package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"

	compute "cloud.google.com/go/compute/apiv1"
	"go.uber.org/zap"

	"github.com/ut0mt8/mig-scaleset/internal/api"
	"github.com/ut0mt8/mig-scaleset/internal/instance"
)

func run(ctx context.Context, opts options) error {
	log, err := newLogger(opts.logLevel, opts.logFormat)
	if err != nil {
		return err
	}
	defer log.Sync() //nolint:errcheck // nothing to do if it fails on exit

	err = runInstances(ctx, opts, log)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("scaler stopped", zap.Error(err))
		return loggedError{err}
	}

	log.Info("shutting down")
	return nil
}

// runInstances starts every instance and waits until ctx is done. A failing
// instance is restarted on its own and never stops the others.
func runInstances(ctx context.Context, opts options, log *zap.Logger) error {
	insts, err := loadInstances(opts)
	if err != nil {
		return err
	}

	// Shared by every instance. Instances show as starting until they run.
	server := api.New(opts.listenAddr, opts.version, log.Named("api"))

	migClient, err := compute.NewRegionInstanceGroupManagersRESTClient(ctx)
	if err != nil {
		return fmt.Errorf("creating GCP client: %w", err)
	}
	defer migClient.Close() //nolint:errcheck // best effort on exit

	runners := make([]*instance.Runner, 0, len(insts))
	for _, inst := range insts {
		runners = append(runners, &instance.Runner{
			Config:  inst,
			Auth:    opts.auth,
			Version: opts.version,
			Compute: migClient,
			Log:     log.With(zap.String("instance", inst.Name)),
			Status: server.Add(api.InstanceInfo{
				Name:     inst.Name,
				URL:      inst.URL,
				ScaleSet: inst.ScaleSet,
				MIG:      inst.MIG,
				Region:   inst.Region,
			}),
		})
	}

	if opts.listenAddr != "" {
		go func() {
			if err := server.Run(ctx); err != nil {
				log.Error("api stopped", zap.Error(err))
			}
		}()
	}

	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Run(ctx)
		}()
	}

	log.Info("instances started", zap.Int("count", len(runners)))
	wg.Wait()
	return ctx.Err()
}
