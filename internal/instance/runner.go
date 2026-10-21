// Package instance runs one scale set on one MIG, and restarts it when it fails.
package instance

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

// Runner runs one instance: its scale set, session, listener and reconcile
// loop.
type Runner struct {
	Config  config.Instance
	Auth    ghclient.Auth
	Version string
	Compute *compute.RegionInstanceGroupManagersClient
	Status  *api.Instance
	Log     *zap.Logger
}

// Run keeps the instance going until ctx is done, restarting it with
// backoff when it fails.
func (r *Runner) Run(ctx context.Context) {
	backoff := minBackoff

	for {
		r.Status.Starting()
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

		r.Status.Failed(err)
		r.Log.Error("instance failed, restarting", zap.Error(err), zap.Duration("in", backoff))

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// attempt runs the instance once. Its reconcile loop stops with it.
func (r *Runner) attempt(ctx context.Context) error {
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
	r.Log.Info(
		"using scale set",
		zap.Int("id", scaleSet.ID),
		zap.String("name", scaleSet.Name),
		zap.Strings("labels", labelNames(scaleSet.Labels)),
	)

	gh.SetSystemInfo(scaleset.SystemInfo{
		System:     "mig-scaler",
		Version:    r.Version,
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
	go s.RunReconciler(ctx, r.Config.ReconcileInterval)

	return r.listen(ctx, gh, s)
}

func (r *Runner) newScaler(gh *scaleset.Client, scaleSetID int) (*scaler.Scaler, error) {
	return scaler.New(scaler.Config{
		GitHub:      gh,
		Compute:     r.Compute,
		ScaleSetID:  scaleSetID,
		Project:     r.Config.Project,
		Region:      r.Config.Region,
		MIG:         r.Config.MIG,
		Prefix:      r.Config.RunnerPrefix,
		WorkFolder:  r.Config.WorkFolder,
		MinRunners:  r.Config.MinRunners,
		MaxRunners:  r.Config.MaxRunners,
		BootTimeout: r.Config.BootTimeout,
		IdleTimeout: r.Config.IdleTimeout,
		MaxAge:      r.Config.MaxRunnerAge,
		Logger:      r.Log.Named("scaler"),
	})
}

// listen opens the GitHub session and handles messages until ctx is done.
func (r *Runner) listen(ctx context.Context, gh *scaleset.Client, s *scaler.Scaler) error {
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
	listenerLog := slog.New(zapslog.NewHandler(r.Log.Core(), zapslog.WithName("listener")))
	l, err := listener.New(
		session,
		listener.Config{
			ScaleSetID: scaleSetID,
			MaxRunners: r.Config.MaxRunners,
			Logger:     listenerLog,
		},
		listener.WithMetricsRecorder(scaler.NewStatsLogger(s)),
	)
	if err != nil {
		return fmt.Errorf("creating listener: %w", err)
	}

	r.Status.Running(s)
	r.Log.Info("listening for jobs")
	return l.Run(ctx, s)
}

// githubClient uses the shared credentials, with the instance overrides.
func (r *Runner) githubClient() (*scaleset.Client, error) {
	auth := r.Auth

	if r.Config.GitHubTokenEnv != "" {
		token := os.Getenv(r.Config.GitHubTokenEnv)
		if token == "" {
			return nil, fmt.Errorf("github_token_env: $%s is empty", r.Config.GitHubTokenEnv)
		}
		auth = ghclient.Auth{Token: token}
	}
	if r.Config.AppInstallationID != 0 {
		auth.AppInstallationID = r.Config.AppInstallationID
	}

	return auth.New(r.Config.URL)
}
