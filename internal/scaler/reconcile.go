package scaler

import (
	"context"
	"slices"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"go.uber.org/zap"
)

// After a JobCompleted, give the ephemeral runner time to deregister before
// treating "not on GitHub" as finished.
const deregisterGrace = time.Minute

type verdict int

const (
	keep     verdict = iota
	dead             // VM unusable: delete it and its registration
	finished         // runner already off GitHub: delete the VM
)

// observed is a tracked runner with its VM, as seen during a pass.
type observed struct {
	runner
	vm *computepb.ManagedInstance
}

// pass holds what one reconcile pass found and did.
type pass struct {
	vms      map[string]*computepb.ManagedInstance
	foreign  []string
	adopted  []string
	pending  []string
	gone     []string
	dead     []string
	finished []string
}

// RunReconciler reconciles every interval until ctx is done. The first pass
// is done by the caller at startup.
func (s *Scaler) RunReconciler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("reconcile failed", zap.Error(err))
			s.monitor.record(EventError, "", "reconcile failed: "+err.Error())
		}
	}
}

// Reconcile makes the MIG, GitHub and our state agree, so a lost message or
// a broken VM can't leak. See check for the rules.
func (s *Scaler) Reconcile(ctx context.Context) error {
	start := time.Now()
	s.refreshStats(ctx)

	vms, foreign, err := s.mig.list(ctx)
	if err != nil {
		return err
	}

	p := &pass{
		vms:      vms,
		foreign:  foreign,
		adopted:  []string{},
		pending:  []string{},
		gone:     []string{},
		dead:     []string{},
		finished: []string{},
	}

	snapshot := s.sync(p)
	for _, name := range p.adopted {
		s.monitor.record(EventAdopted, name, "")
	}

	if len(p.pending)+len(p.gone) > 0 {
		s.logMIGErrors(ctx, slices.Concat(p.pending, p.gone))
	}
	if len(p.gone) > 0 {
		s.forget(p.gone...)
		s.deregisterAll(ctx, p.gone, "VM never appeared")
	}

	for name, obs := range snapshot {
		switch s.check(ctx, name, obs) {
		case dead:
			p.dead = append(p.dead, name)
		case finished:
			p.finished = append(p.finished, name)
		}
	}

	if err := s.deleteVMs(ctx, p); err != nil {
		return err
	}

	s.logPass(p, time.Since(start))
	return nil
}

// sync adopts unknown VMs, sorts tracked runners without a VM into pending
// and gone, and returns a copy of the runners that have a VM.
func (s *Scaler) sync(p *pass) map[string]observed {
	now := time.Now()
	snapshot := map[string]observed{}

	s.mu.Lock()
	defer s.mu.Unlock()

	for name, vm := range p.vms {
		if _, ok := s.runners[name]; ok {
			continue
		}
		s.log.Info("adopting VM", zap.String("name", name))
		s.runners[name] = &runner{created: now, instanceID: vm.GetId()}
		p.adopted = append(p.adopted, name)
	}

	for name, r := range s.runners {
		vm, ok := p.vms[name]
		if !ok {
			if now.Sub(r.created) > s.cfg.BootTimeout {
				p.gone = append(p.gone, name)
			} else {
				p.pending = append(p.pending, name)
			}
			continue
		}

		if r.instanceID == 0 {
			r.instanceID = vm.GetId()
		}
		r.vmSeen = true
		snapshot[name] = observed{runner: *r, vm: vm}
	}

	return snapshot
}

func (s *Scaler) check(ctx context.Context, name string, r observed) verdict {
	vm := r.vm
	age := time.Since(r.created)
	recreated := vm.GetId() != 0 && vm.GetId() != r.instanceID

	switch {
	case recreated:
		s.log.Warn("VM recreated by the MIG, its JIT config is spent", zap.String("name", name))
		return dead
	case isDown(vm.GetInstanceStatus()):
		s.log.Warn("VM is down", zap.String("name", name), zap.String("status", vm.GetInstanceStatus()))
		return dead
	case age > s.cfg.MaxAge:
		s.log.Warn(
			"runner too old",
			zap.String("name", name),
			zap.Duration("age", age),
			zap.Bool("busy", r.busy),
		)
		return dead
	}

	ref, err := s.gh.GetRunnerByName(ctx, name)
	if err != nil {
		s.log.Warn("runner lookup failed", zap.String("name", name), zap.Error(err))
		return keep
	}

	// JIT runners are registered at creation, so a missing one has finished.
	if ref == nil {
		if age < deregisterGrace {
			return keep
		}
		s.log.Info("runner gone from GitHub", zap.String("name", name))
		return finished
	}

	if r.busy || age <= s.cfg.IdleTimeout {
		return keep
	}

	// Idle for too long: it never connected, or we missed its JobStarted.
	removed, err := s.tryDeregister(ctx, name, "idle timeout")
	if err != nil {
		s.log.Warn("could not deregister runner", zap.String("name", name), zap.Error(err))
		return keep
	}
	if !removed {
		s.log.Info("runner is actually busy, keeping it", zap.String("name", name))
		return keep
	}

	return finished
}

func (s *Scaler) deleteVMs(ctx context.Context, p *pass) error {
	names := slices.Concat(p.dead, p.finished)
	if len(names) == 0 {
		return nil
	}

	urls := make([]string, 0, len(names))
	for _, name := range names {
		urls = append(urls, p.vms[name].GetInstance())
	}

	if err := s.mig.delete(ctx, urls); err != nil {
		return err
	}
	for _, name := range names {
		s.monitor.record(EventVMDeleted, name, "reconcile")
	}

	s.forget(names...)
	s.deregisterAll(ctx, p.dead, "VM dead or too old")
	return nil
}

// refreshStats reads the scale set statistics from the API. GitHub only
// pushes them with job messages, so they go stale when nothing happens.
func (s *Scaler) refreshStats(ctx context.Context) {
	scaleSet, err := s.gh.GetRunnerScaleSetByID(ctx, s.cfg.ScaleSetID)
	if err != nil {
		s.log.Warn("could not read scale set statistics", zap.Error(err))
		return
	}
	if scaleSet == nil || scaleSet.Statistics == nil {
		return
	}
	s.monitor.setStats(scaleSet.Statistics)
}

func (s *Scaler) logMIGErrors(ctx context.Context, names []string) {
	migErrors, err := s.mig.lastErrors(ctx, names)
	if err != nil {
		s.log.Warn("could not list MIG errors", zap.Error(err))
		return
	}

	for name, migErr := range migErrors {
		s.monitor.record(EventError, name, migErr.GetError().GetMessage())
		s.log.Warn(
			"MIG error for runner VM",
			zap.String("name", name),
			zap.String("action", migErr.GetInstanceActionDetails().GetAction()),
			zap.String("code", migErr.GetError().GetCode()),
			zap.String("message", migErr.GetError().GetMessage()),
			zap.String("at", migErr.GetTimestamp()),
		)
	}
}

func (s *Scaler) logPass(p *pass, took time.Duration) {
	s.monitor.setPass(PassSummary{
		At:              time.Now(),
		TookMS:          took.Milliseconds(),
		VMsInMIG:        len(p.vms),
		Adopted:         p.adopted,
		Pending:         p.pending,
		Forgotten:       p.gone,
		DeletedDead:     p.dead,
		DeletedFinished: p.finished,
		ForeignVMs:      p.foreign,
	})

	tracked, busy := s.runnerCounts()

	fields := []zap.Field{
		zap.Int("vms_in_mig", len(p.vms)),
		zap.Int("tracked", tracked),
		zap.Int("busy", busy),
		zap.Int("idle", tracked-busy-len(p.pending)),
		zap.Int("pending_creation", len(p.pending)),
		zap.Duration("took", took),
	}
	if len(p.pending) > 0 {
		fields = append(fields, zap.Strings("pending_names", p.pending))
	}
	if len(p.foreign) > 0 {
		fields = append(fields, zap.Strings("ignored_foreign_vms", p.foreign))
	}

	acted := len(p.adopted) + len(p.gone) + len(p.dead) + len(p.finished)
	if acted == 0 {
		s.log.Info("reconcile: nothing to do", fields...)
		return
	}

	fields = append(
		fields,
		zap.Strings("adopted", p.adopted),
		zap.Strings("forgotten_no_vm", p.gone),
		zap.Strings("deleted_dead", p.dead),
		zap.Strings("deleted_finished", p.finished),
	)
	s.log.Info("reconcile: actions taken", fields...)
}
