package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ut0mt8/mig-scaleset/internal/config"
)

func TestSingleInstanceFromFlags(t *testing.T) {
	opts := options{base: config.Instance{
		URL:               "https://github.com/org",
		ScaleSet:          "gcp-runners",
		Project:           "p",
		Region:            "r",
		MIG:               "m",
		RunnerPrefix:      "gh-runner",
		MaxRunners:        3,
		ReconcileInterval: time.Minute,
		BootTimeout:       5 * time.Minute,
		IdleTimeout:       30 * time.Minute,
		MaxRunnerAge:      7 * time.Hour,
	}}

	insts, err := loadInstances(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 1 || insts[0].Name != "gcp-runners" {
		t.Fatalf("got %+v, want one instance named after the scale set", insts)
	}
}

func TestMissingFlagsWithoutConfig(t *testing.T) {
	_, err := loadInstances(options{base: config.Instance{MaxRunners: 3}})
	if err == nil || !strings.Contains(err.Error(), "--config") {
		t.Fatalf("got %v, want a hint about the flags or --config", err)
	}
}
