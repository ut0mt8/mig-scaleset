package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/actions/scaleset"
	"github.com/spf13/cobra"

	"github.com/ut0mt8/mig-scaleset/internal/config"
	"github.com/ut0mt8/mig-scaleset/internal/ghclient"
	"github.com/ut0mt8/mig-scaleset/internal/settings"
)

type options struct {
	version    string
	configPath string

	// One instance from the flags, or the defaults of every instance of
	// the config file.
	base config.Instance
	auth ghclient.Auth

	logLevel   string
	logFormat  string
	listenAddr string
}

func Execute(version string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := newRootCommand(version).ExecuteContext(ctx)
	if err == nil {
		return
	}

	// Errors from the scaler are already in the logs.
	var logged loggedError
	if !errors.As(err, &logged) {
		fmt.Fprintln(os.Stderr, "Error:", err)
	}
	os.Exit(1)
}

type loggedError struct{ error }

// defaultListenAddr follows $PORT when set, like on Cloud Run.
func defaultListenAddr() string {
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return ":8080"
}

func newRootCommand(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mig-scaler",
		Short: "Autoscale GitHub Actions scale set runners on regional GCP MIGs",
		Long: `Autoscale GitHub Actions scale set runners on regional GCP MIGs.

Run one instance from the flags, or several from --config. With --config,
the instance flags are defaults for every instance of the file.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := readOptions(cmd, version)
			if err != nil {
				return err
			}
			return run(cmd.Context(), opts)
		},
	}

	f := cmd.Flags()

	// Flags stay one per line so they read as a table.
	f.String("config", "", "TOML file with several instances")

	f.String("url", "", "GitHub org, repo or enterprise URL")
	f.String("name", "", "scale set name, used in runs-on")
	f.StringSlice("labels", []string{}, "extra scale set labels, comma separated")
	f.String("runner-group", scaleset.DefaultRunnerGroup, "runner group")

	f.String("project", "", "GCP project")
	f.String("region", "", "region of the MIG")
	f.String("mig", "", "managed instance group name")
	f.String("runner-prefix", "gh-runner", "prefix of VM and runner names")
	f.String("work-folder", "", "runner work folder on the VM (default _work)")

	f.Int("min-runners", 0, "idle runners to keep warm")
	f.Int("max-runners", 10, "maximum number of runners")

	f.Duration("reconcile-interval", 2*time.Minute, "time between reconcile passes")
	f.Duration("boot-timeout", 10*time.Minute, "max time for a new VM to show up")
	f.Duration("idle-timeout", 30*time.Minute, "recycle runners idle for longer")
	f.Duration("max-runner-age", 7*time.Hour, "delete any runner older, even busy")

	f.String("log-level", "info", "debug, info, warn or error")
	f.String("log-format", "json", "json or console")
	f.String("listen-addr", defaultListenAddr(), "API and UI address, empty to disable")

	ghclient.AddFlags(f)
	settings.Document(f, settingsOptions)

	return cmd
}

var settingsOptions = settings.Options{Aliases: ghclient.EnvAliases}

// readOptions resolves every flag with its env var through viper.
func readOptions(cmd *cobra.Command, version string) (options, error) {
	r, err := settings.Bind(cmd.Flags(), settingsOptions)
	if err != nil {
		return options{}, err
	}

	opts := options{
		version:    version,
		configPath: r.String("config"),
		auth:       ghclient.AuthFrom(r),
		logLevel:   r.String("log-level"),
		logFormat:  r.String("log-format"),
		listenAddr: r.String("listen-addr"),
		base: config.Instance{
			URL:               r.String("url"),
			ScaleSet:          r.String("name"),
			Labels:            r.Strings("labels"),
			RunnerGroup:       r.String("runner-group"),
			Project:           r.String("project"),
			Region:            r.String("region"),
			MIG:               r.String("mig"),
			RunnerPrefix:      r.String("runner-prefix"),
			WorkFolder:        r.String("work-folder"),
			MinRunners:        r.Int("min-runners"),
			MaxRunners:        r.Int("max-runners"),
			ReconcileInterval: r.Duration("reconcile-interval"),
			BootTimeout:       r.Duration("boot-timeout"),
			IdleTimeout:       r.Duration("idle-timeout"),
			MaxRunnerAge:      r.Duration("max-runner-age"),
		},
	}
	if err := r.Err(); err != nil {
		return options{}, err
	}
	return opts, nil
}

// loadInstances returns the instances from --config, or the single
// instance described by the flags.
func loadInstances(opts options) ([]config.Instance, error) {
	if opts.configPath != "" {
		return config.Load(opts.configPath, opts.base)
	}

	single := opts.base
	single.Name = single.ScaleSet
	if err := config.Validate([]config.Instance{single}); err != nil {
		return nil, fmt.Errorf("without --config, the flags describe one instance: %w", err)
	}
	return []config.Instance{single}, nil
}
