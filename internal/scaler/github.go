package scaler

import (
	"context"
	"errors"
	"fmt"

	"github.com/actions/scaleset"
	"go.uber.org/zap"
)

// register creates the runner on GitHub and returns its JIT config.
func (s *Scaler) register(ctx context.Context, name string) (string, error) {
	setting := &scaleset.RunnerScaleSetJitRunnerSetting{
		Name:       name,
		WorkFolder: s.cfg.WorkFolder,
	}

	jit, err := s.gh.GenerateJitRunnerConfig(ctx, setting, s.cfg.ScaleSetID)
	if err != nil {
		return "", fmt.Errorf("generating JIT config for %q: %w", name, err)
	}

	if jit.Runner != nil {
		s.log.Info("runner registered", zap.String("name", name), zap.Int("id", jit.Runner.ID))
		s.monitor.record(EventRegistered, name, "")
	}
	return jit.EncodedJITConfig, nil
}

// tryDeregister removes an idle runner from GitHub. It returns false, and
// marks the runner busy, when GitHub says it is running a job.
func (s *Scaler) tryDeregister(ctx context.Context, name, reason string) (bool, error) {
	ref, err := s.gh.GetRunnerByName(ctx, name)
	if err != nil {
		return false, fmt.Errorf("looking up runner %q: %w", name, err)
	}
	if ref == nil {
		return true, nil
	}

	err = s.gh.RemoveRunner(ctx, int64(ref.ID))
	switch {
	case errors.Is(err, scaleset.JobStillRunningError):
		s.markBusy(name)
		return false, nil
	case err != nil && !errors.Is(err, scaleset.RunnerNotFoundError):
		return false, fmt.Errorf("removing runner %q: %w", name, err)
	}

	s.monitor.record(EventDeregistered, name, reason)
	s.log.Info(
		"runner deregistered",
		zap.String("name", name),
		zap.Int("id", ref.ID),
		zap.String("reason", reason),
	)
	return true, nil
}

// deregisterAll is best effort, for runners whose VM is gone or never came.
func (s *Scaler) deregisterAll(ctx context.Context, names []string, reason string) {
	for _, name := range names {
		removed, err := s.tryDeregister(ctx, name, reason)
		if err != nil {
			s.log.Warn("could not deregister runner", zap.String("name", name), zap.Error(err))
			continue
		}
		if !removed {
			s.log.Info("runner still has a job, GitHub will drop it", zap.String("name", name))
		}
	}
}
