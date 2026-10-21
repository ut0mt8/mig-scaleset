package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/actions/scaleset"
	"go.uber.org/zap"

	"github.com/ut0mt8/mig-scaleset/internal/ghclient"
)

// ensureScaleSet returns the scale set of the instance, creating it on
// first run and updating its labels when they changed.
func (r *instanceRunner) ensureScaleSet(ctx context.Context, gh *scaleset.Client) (*scaleset.RunnerScaleSet, error) {
	cfg := r.cfg

	groupID, err := ghclient.RunnerGroupID(ctx, gh, cfg.RunnerGroup)
	if err != nil {
		return nil, err
	}

	existing, err := gh.GetRunnerScaleSet(ctx, groupID, cfg.ScaleSet)
	if err != nil {
		return nil, fmt.Errorf("getting scale set %q: %w", cfg.ScaleSet, err)
	}

	wanted := &scaleset.RunnerScaleSet{
		Name:          cfg.ScaleSet,
		RunnerGroupID: groupID,
		Labels:        buildLabels(cfg.ScaleSet, cfg.Labels),
		RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true},
	}

	if existing == nil {
		created, err := gh.CreateRunnerScaleSet(ctx, wanted)
		if err != nil {
			return nil, fmt.Errorf("creating scale set %q: %w", cfg.ScaleSet, err)
		}
		return created, nil
	}

	if sameLabels(existing.Labels, wanted.Labels) {
		return existing, nil
	}

	r.log.Info(
		"updating scale set labels",
		zap.Strings("from", labelNames(existing.Labels)),
		zap.Strings("to", labelNames(wanted.Labels)),
	)
	updated, err := gh.UpdateRunnerScaleSet(ctx, existing.ID, wanted)
	if err != nil {
		return nil, fmt.Errorf("updating labels of scale set %q: %w", cfg.ScaleSet, err)
	}
	return updated, nil
}

// buildLabels always keeps the scale set name, so runs-on: <name> works.
func buildLabels(name string, extra []string) []scaleset.Label {
	labels := []scaleset.Label{{Type: "System", Name: name}}
	seen := map[string]bool{strings.ToLower(name): true}

	for _, label := range extra {
		label = strings.TrimSpace(label)
		key := strings.ToLower(label)
		if label == "" || seen[key] {
			continue
		}
		seen[key] = true
		labels = append(labels, scaleset.Label{Type: "System", Name: label})
	}
	return labels
}

// sameLabels compares label names, ignoring order and case like GitHub.
func sameLabels(a, b []scaleset.Label) bool {
	normalize := func(labels []scaleset.Label) []string {
		names := labelNames(labels)
		for i := range names {
			names[i] = strings.ToLower(names[i])
		}
		slices.Sort(names)
		return names
	}
	return slices.Equal(normalize(a), normalize(b))
}

func labelNames(labels []scaleset.Label) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		names = append(names, label.Name)
	}
	return names
}
