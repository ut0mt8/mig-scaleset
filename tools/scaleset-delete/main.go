// scaleset-delete removes a runner scale set from GitHub, for example one
// left behind by a test or an old deployment.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/actions/scaleset"
	"github.com/spf13/cobra"

	"github.com/ut0mt8/mig-scaleset/internal/ghclient"
	"github.com/ut0mt8/mig-scaleset/internal/settings"
)

type options struct {
	githubURL   string
	name        string
	runnerGroup string
	id          int
	yes         bool
	force       bool
	auth        ghclient.Auth
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// Deleting must be asked for on the command line, never via env.
var settingsOptions = settings.Options{
	Skip:    []string{"yes", "force"},
	Aliases: ghclient.EnvAliases,
}

func newCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scaleset-delete",
		Short: "Delete a GitHub Actions runner scale set",
		Long: `Delete a GitHub Actions runner scale set.

Without --yes it only shows the scale set. It refuses to delete a scale set
that has jobs or busy runners, unless --force is given. VMs in the MIG are
not touched, delete them separately.`,
		Example: `  scaleset-delete --url https://github.com/org-hq --name test
  scaleset-delete --url https://github.com/org-hq --id 2 --yes`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := readOptions(cmd)
			if err != nil {
				return err
			}
			return run(cmd.Context(), opts, cmd.OutOrStdout())
		},
	}

	f := cmd.Flags()
	f.String("url", "", "GitHub org, repo or enterprise URL")
	f.String("name", "", "scale set name")
	f.String("runner-group", scaleset.DefaultRunnerGroup, "runner group of the scale set")
	f.Int("id", 0, "scale set ID, instead of --name")
	f.Bool("yes", false, "really delete")
	f.Bool("force", false, "delete even if jobs or busy runners are reported")

	ghclient.AddFlags(f)
	settings.Document(f, settingsOptions)

	return cmd
}

func readOptions(cmd *cobra.Command) (options, error) {
	r, err := settings.Bind(cmd.Flags(), settingsOptions)
	if err != nil {
		return options{}, err
	}

	opts := options{
		githubURL:   r.String("url"),
		name:        r.String("name"),
		runnerGroup: r.String("runner-group"),
		id:          r.Int("id"),
		auth:        ghclient.AuthFrom(r),
	}
	if err := r.Err(); err != nil {
		return options{}, err
	}

	// Read straight from the command line, see settingsOptions.
	opts.yes, _ = cmd.Flags().GetBool("yes")     //nolint:errcheck // the flag exists
	opts.force, _ = cmd.Flags().GetBool("force") //nolint:errcheck // the flag exists

	switch {
	case opts.githubURL == "":
		return options{}, errors.New("--url is required")
	case opts.name == "" && opts.id == 0:
		return options{}, errors.New("give --name or --id")
	case opts.name != "" && opts.id != 0:
		return options{}, errors.New("give --name or --id, not both")
	}
	return opts, nil
}

func run(ctx context.Context, opts options, out io.Writer) error {
	gh, err := opts.auth.New(opts.githubURL)
	if err != nil {
		return err
	}

	scaleSet, err := find(ctx, gh, opts)
	if err != nil {
		return err
	}

	describe(out, scaleSet)

	if inUse(scaleSet) && !opts.force {
		return errors.New("the scale set reports jobs or busy runners, use --force to delete it anyway")
	}

	if !opts.yes {
		fmt.Fprintln(out, "\nDry run, nothing deleted. Add --yes to delete it.")
		return nil
	}

	if err := gh.DeleteRunnerScaleSet(ctx, scaleSet.ID); err != nil {
		return fmt.Errorf("deleting scale set %d: %w", scaleSet.ID, err)
	}

	fmt.Fprintf(
		out,
		"\nDeleted scale set %d (%s).\n",
		scaleSet.ID,
		scaleSet.Name,
	)
	return nil
}

func find(ctx context.Context, gh *scaleset.Client, opts options) (*scaleset.RunnerScaleSet, error) {
	if opts.id != 0 {
		scaleSet, err := gh.GetRunnerScaleSetByID(ctx, opts.id)
		if err != nil {
			return nil, fmt.Errorf("getting scale set %d: %w", opts.id, err)
		}
		if scaleSet == nil {
			return nil, fmt.Errorf("no scale set with ID %d", opts.id)
		}
		return scaleSet, nil
	}

	groupID, err := ghclient.RunnerGroupID(ctx, gh, opts.runnerGroup)
	if err != nil {
		return nil, err
	}

	scaleSet, err := gh.GetRunnerScaleSet(ctx, groupID, opts.name)
	if err != nil {
		return nil, fmt.Errorf("getting scale set %q: %w", opts.name, err)
	}
	if scaleSet == nil {
		return nil, fmt.Errorf("no scale set %q in runner group %q", opts.name, opts.runnerGroup)
	}
	return scaleSet, nil
}

func describe(out io.Writer, scaleSet *scaleset.RunnerScaleSet) {
	line := func(format string, args ...any) {
		fmt.Fprintf(out, format+"\n", args...)
	}

	labels := make([]string, 0, len(scaleSet.Labels))
	for _, label := range scaleSet.Labels {
		labels = append(labels, label.Name)
	}

	line("Scale set %d: %s", scaleSet.ID, scaleSet.Name)
	line("  runner group: %s (%d)", scaleSet.RunnerGroupName, scaleSet.RunnerGroupID)
	line("  labels:       %s", strings.Join(labels, ", "))
	if !scaleSet.CreatedOn.IsZero() {
		line("  created:      %s", scaleSet.CreatedOn.Format("2006-01-02 15:04"))
	}

	stats := scaleSet.Statistics
	if stats == nil {
		line("  statistics:   not reported")
		return
	}
	line("  jobs:         %d assigned, %d running", stats.TotalAssignedJobs, stats.TotalRunningJobs)
	line(
		"  runners:      %d registered, %d busy, %d idle",
		stats.TotalRegisteredRunners,
		stats.TotalBusyRunners,
		stats.TotalIdleRunners,
	)
}

func inUse(scaleSet *scaleset.RunnerScaleSet) bool {
	stats := scaleSet.Statistics
	if stats == nil {
		return false
	}
	return stats.TotalAssignedJobs > 0 || stats.TotalBusyRunners > 0
}
