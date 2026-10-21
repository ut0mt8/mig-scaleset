package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"

	"github.com/ut0mt8/mig-scaleset/internal/api"
	"github.com/ut0mt8/mig-scaleset/internal/config"
	"github.com/ut0mt8/mig-scaleset/internal/ghclient"
	"github.com/ut0mt8/mig-scaleset/internal/scaler"
)

const (
	minBackoff = 10 * time.Second
	maxBackoff = 5 * time.Minute
	// An attempt that ran this long resets the backoff.
	healthyRun = 10 * time.Minute
)

// instanceRunner runs one instance: its scale set, session, listener and
// reconcile loop.
type instanceRunner struct {
	cfg     config.Instance
	auth    ghclient.Auth
	version string
	compute *compute.RegionInstanceGroupManagersClient
	status  *api.Instance
	log     *zap.Logger
}

// run keeps the instance going until ctx is done, restarting it with
// backoff when it fails.
func (r *instanceRunner) run(ctx context.Context) {
	backoff := minBackoff

	for {
		r.status.Starting()
		started := time.Now()

		err := r.attempt(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("listener stopped")
		}
		if time.Since(started) > healthyRun {
			backoff = minBackoff
		}

		r.status.Failed(err)
		r.log.Error("instance failed, restarting", zap.Error(err), zap.Duration("in", backoff))

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// attempt runs the instance once. Its reconcile loop stops with it.
func (r *instanceRunner) attempt(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	gh, err := r.githubClient()
	if err != nil {
		return err
	}

	scaleSet, err := r.ensureScaleSet(ctx, gh)
	if err != nil {
		return err
	}
	r.log.Info(
		"using scale set",
		zap.Int("id", scaleSet.ID),
		zap.String("name", scaleSet.Name),
		zap.Strings("labels", labelNames(scaleSet.Labels)),
	)

	gh.SetSystemInfo(scaleset.SystemInfo{
		System:     "mig-scaler",
		Version:    r.version,
		ScaleSetID: scaleSet.ID,
	})

	s, err := r.newScaler(gh, scaleSet.ID)
	if err != nil {
		return err
	}

	// Adopt VMs left by a previous run before taking new jobs.
	if err := s.Reconcile(ctx); err != nil {
		return fmt.Errorf("initial reconcile: %w", err)
	}
	go s.RunReconciler(ctx, r.cfg.ReconcileInterval)

	return r.listen(ctx, gh, s)
}

func (r *instanceRunner) newScaler(gh *scaleset.Client, scaleSetID int) (*scaler.Scaler, error) {
	return scaler.New(scaler.Config{
		GitHub:      gh,
		Compute:     r.compute,
		ScaleSetID:  scaleSetID,
		Project:     r.cfg.Project,
		Region:      r.cfg.Region,
		MIG:         r.cfg.MIG,
		Prefix:      r.cfg.RunnerPrefix,
		WorkFolder:  r.cfg.WorkFolder,
		MinRunners:  r.cfg.MinRunners,
		MaxRunners:  r.cfg.MaxRunners,
		BootTimeout: r.cfg.BootTimeout,
		IdleTimeout: r.cfg.IdleTimeout,
		MaxAge:      r.cfg.MaxRunnerAge,
		Logger:      r.log.Named("scaler"),
	})
}

// listen opens the GitHub session and handles messages until ctx is done.
func (r *instanceRunner) listen(ctx context.Context, gh *scaleset.Client, s *scaler.Scaler) error {
	scaleSetID := s.ScaleSetID()

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("getting hostname: %w", err)
	}

	session, err := gh.MessageSessionClient(ctx, scaleSetID, hostname)
	if err != nil {
		return fmt.Errorf("creating message session: %w", err)
	}
	defer session.Close(context.WithoutCancel(ctx)) //nolint:errcheck // best effort on exit

	// The listener logs through slog, send it to zap.
	listenerLog := slog.New(zapslog.NewHandler(r.log.Core(), zapslog.WithName("listener")))
	l, err := listener.New(
		session,
		listener.Config{
			ScaleSetID: scaleSetID,
			MaxRunners: r.cfg.MaxRunners,
			Logger:     listenerLog,
		},
		listener.WithMetricsRecorder(scaler.NewStatsLogger(s)),
	)
	if err != nil {
		return fmt.Errorf("creating listener: %w", err)
	}

	r.status.Running(s)
	r.log.Info("listening for jobs")
	return l.Run(ctx, s)
}

// githubClient uses the shared credentials, with the instance overrides.
func (r *instanceRunner) githubClient() (*scaleset.Client, error) {
	auth := r.auth

	if r.cfg.GitHubTokenEnv != "" {
		token := os.Getenv(r.cfg.GitHubTokenEnv)
		if token == "" {
			return nil, fmt.Errorf("github_token_env: $%s is empty", r.cfg.GitHubTokenEnv)
		}
		auth = ghclient.Auth{Token: token}
	}
	if r.cfg.AppInstallationID != 0 {
		auth.AppInstallationID = r.cfg.AppInstallationID
	}

	return auth.New(r.cfg.URL)
}
