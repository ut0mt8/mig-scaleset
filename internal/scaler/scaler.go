// Package scaler runs GitHub Actions scale set runners on a regional GCP MIG.
// Each runner is one VM; its single-use JIT config is passed through
// per-instance metadata.
package scaler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type Config struct {
	GitHub     *scaleset.Client
	Compute    *compute.RegionInstanceGroupManagersClient
	ScaleSetID int

	Project string
	Region  string
	MIG     string
	Prefix  string

	// Empty means the runner default (_work).
	WorkFolder string

	MinRunners int
	MaxRunners int

	BootTimeout time.Duration
	IdleTimeout time.Duration
	MaxAge      time.Duration

	Logger *zap.Logger
}

type Scaler struct {
	cfg Config
	gh  *scaleset.Client
	mig *mig
	log *zap.Logger

	mu      sync.Mutex
	runners map[string]*runner // by VM name, which is also the runner name

	monitor monitor
}

var _ listener.Scaler = (*Scaler)(nil)

func New(cfg Config) (*Scaler, error) {
	switch {
	case cfg.GitHub == nil || cfg.Compute == nil:
		return nil, errors.New("GitHub and Compute clients are required")
	case cfg.MaxRunners < cfg.MinRunners:
		return nil, fmt.Errorf("max runners (%d) is lower than min runners (%d)", cfg.MaxRunners, cfg.MinRunners)
	case cfg.Prefix == "":
		return nil, errors.New("runner prefix is required")
	}

	log := cfg.Logger
	if log == nil {
		log = zap.NewNop()
	}

	return &Scaler{
		cfg: cfg,
		gh:  cfg.GitHub,
		mig: &mig{
			client:  cfg.Compute,
			project: cfg.Project,
			region:  cfg.Region,
			name:    cfg.MIG,
			prefix:  cfg.Prefix,
		},
		log:     log,
		runners: map[string]*runner{},
		monitor: newMonitor(),
	}, nil
}

func (s *Scaler) ScaleSetID() int {
	return s.cfg.ScaleSetID
}

// HandleDesiredRunnerCount is called on every message and every empty poll
// (~50s), so errors are only logged: the next call retries.
func (s *Scaler) HandleDesiredRunnerCount(ctx context.Context, assigned int) (int, error) {
	target := min(s.cfg.MaxRunners, s.cfg.MinRunners+assigned)
	current := s.count()

	switch {
	case target > current:
		if err := s.scaleUp(ctx, target-current); err != nil {
			s.log.Error("scale up failed", zap.Error(err))
		}
	case target < current:
		if err := s.scaleDown(ctx, current-target); err != nil {
			s.log.Error("scale down failed", zap.Error(err))
		}
	}

	return s.count(), nil
}

func (s *Scaler) HandleJobStarted(_ context.Context, job *scaleset.JobStarted) error {
	s.markBusy(job.RunnerName)
	s.setJob(job.RunnerName, job.JobDisplayName)
	s.monitor.record(EventJobStarted, job.RunnerName, job.JobDisplayName)
	s.log.Info(
		"job started",
		zap.String("runner", job.RunnerName),
		zap.String("job", job.JobDisplayName),
	)
	return nil
}

// HandleJobCompleted runs for every result. Errors are only logged: the
// message is already acked and the reconciler finishes the cleanup.
func (s *Scaler) HandleJobCompleted(ctx context.Context, job *scaleset.JobCompleted) error {
	// A job canceled before assignment has no runner.
	if job.RunnerName == "" || !s.tracks(job.RunnerName) {
		return nil
	}

	s.monitor.record(EventJobCompleted, job.RunnerName, job.Result)
	s.log.Info(
		"job completed, deleting VM",
		zap.String("runner", job.RunnerName),
		zap.String("result", job.Result),
	)

	if err := s.mig.deleteByName(ctx, []string{job.RunnerName}); err != nil {
		s.log.Error("deleting VM failed", zap.String("runner", job.RunnerName), zap.Error(err))
		return nil
	}

	s.forget(job.RunnerName)
	s.monitor.record(EventVMDeleted, job.RunnerName, "job completed")
	return nil
}

func (s *Scaler) scaleUp(ctx context.Context, count int) error {
	s.log.Info("scaling up", zap.Int("add", count), zap.Int("current", s.count()))
	s.monitor.record(EventScaleUp, "", fmt.Sprintf("+%d runners", count))

	names := make([]string, 0, count)
	configs := make([]*computepb.PerInstanceConfig, 0, count)

	for range count {
		name := fmt.Sprintf("%s-%s", s.cfg.Prefix, uuid.NewString()[:8])

		jitConfig, err := s.register(ctx, name)
		if err != nil {
			s.deregisterAll(ctx, names, "scale up aborted")
			return err
		}

		names = append(names, name)
		configs = append(configs, &computepb.PerInstanceConfig{
			Name: proto.String(name),
			PreservedState: &computepb.PreservedState{
				Metadata: map[string]string{"jit-config": jitConfig},
			},
		})
	}

	op, err := s.mig.create(ctx, configs)
	if err != nil {
		s.deregisterAll(ctx, names, "createInstances failed")
		return err
	}

	now := time.Now()
	s.mu.Lock()
	for _, name := range names {
		s.runners[name] = &runner{created: now}
	}
	s.mu.Unlock()

	// Creating VMs is slow, don't block the listener.
	go s.waitCreate(context.WithoutCancel(ctx), op, names)

	return nil
}

func (s *Scaler) waitCreate(ctx context.Context, op *compute.Operation, names []string) {
	if err := waitOperation(ctx, op); err != nil {
		s.log.Error("createInstances failed", zap.Strings("names", names), zap.Error(err))
		s.monitor.record(EventError, "", "createInstances failed: "+err.Error())
		s.forget(names...)
		s.deregisterAll(ctx, names, "createInstances failed")
	}
}

// scaleDown removes up to count idle runners, newest first. GitHub refuses
// to remove a runner that just took a job, so no job is killed.
func (s *Scaler) scaleDown(ctx context.Context, count int) error {
	removed := make([]string, 0, count)

	for _, name := range s.idleNewestFirst() {
		if len(removed) == count {
			break
		}

		ok, err := s.tryDeregister(ctx, name, "scale down")
		if err != nil {
			s.log.Warn("could not deregister runner", zap.String("name", name), zap.Error(err))
			continue
		}
		if !ok {
			s.log.Info("runner just took a job, keeping it", zap.String("name", name))
			continue
		}
		removed = append(removed, name)
	}

	if len(removed) == 0 {
		return nil
	}

	s.log.Info("scaling down", zap.Strings("remove", removed), zap.Int("current", s.count()))
	s.monitor.record(EventScaleDown, "", fmt.Sprintf("-%d runners", len(removed)))

	// If the delete fails, the reconciler adopts the VM, sees no runner on
	// GitHub and deletes it.
	s.forget(removed...)
	return s.mig.deleteByName(ctx, removed)
}
