package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func baseInstance() Instance {
	return Instance{
		RunnerGroup:       "default",
		RunnerPrefix:      "gh-runner",
		MaxRunners:        10,
		ReconcileInterval: 2 * time.Minute,
		BootTimeout:       10 * time.Minute,
		IdleTimeout:       30 * time.Minute,
		MaxRunnerAge:      7 * time.Hour,
		Labels:            []string{},
	}
}

func TestLoadMergesBaseDefaultsAndInstance(t *testing.T) {
	path := writeConfig(t, `
[defaults]
project = "shared-project"
region = "us-central1"
max_runners = 5
labels = ["shared-1", "shared-2", "shared-3"]

[[instances]]
name = "org-a"
url = "https://github.com/org-a"
scale_set = "gcp-runners"
mig = "runners-a"
labels = ["linux"]

[[instances]]
name = "org-b"
url = "https://github.com/org-b"
scale_set = "gcp-runners"
mig = "runners-b"
max_runners = 20
reconcile_interval = "30s"
app_installation_id = 42
`)

	instances, err := Load(path, baseInstance())
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 {
		t.Fatalf("got %d instances, want 2", len(instances))
	}

	a, b := instances[0], instances[1]
	if a.Project != "shared-project" || a.MaxRunners != 5 || a.RunnerPrefix != "gh-runner" {
		t.Errorf("org-a did not get the defaults: %+v", a)
	}
	if strings.Join(a.Labels, "|") != "linux" {
		t.Errorf("org-a labels must replace the defaults: %v", a.Labels)
	}
	if strings.Join(b.Labels, "|") != "shared-1|shared-2|shared-3" {
		t.Errorf("org-b must inherit the default labels: %v", b.Labels)
	}
	if b.MaxRunners != 20 || b.ReconcileInterval != 30*time.Second || b.AppInstallationID != 42 {
		t.Errorf("org-b overrides not applied: %+v", b)
	}
}

func TestValidateCatchesClashes(t *testing.T) {
	path := writeConfig(t, `
[defaults]
project = "p"
region = "r"

[[instances]]
name = "one"
url = "https://github.com/org"
scale_set = "same"
mig = "shared"

[[instances]]
name = "one"
url = "https://github.com/org"
scale_set = "same"
mig = "shared"

[[instances]]
name = "incomplete"
`)

	_, err := Load(path, baseInstance())
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"same name", "same scale set", "share a MIG", "missing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestUnknownKeysAreRejected(t *testing.T) {
	path := writeConfig(t, `
[[instances]]
name = "a"
url = "https://github.com/org"
scale_set = "s"
project = "p"
region = "r"
mig = "m"
max_runner = 5
`)

	_, err := Load(path, baseInstance())
	if err == nil || !strings.Contains(err.Error(), "max_runner") {
		t.Fatalf("got %v, want an error naming the misspelled key", err)
	}
}

func TestBadDuration(t *testing.T) {
	path := writeConfig(t, `
[[instances]]
name = "a"
idle_timeout = "soon"
`)

	if _, err := Load(path, baseInstance()); err == nil {
		t.Fatal("expected an error for an invalid duration")
	}
}

func TestUnknownSection(t *testing.T) {
	path := writeConfig(t, `
[instance]
name = "a"
`)

	_, err := Load(path, baseInstance())
	if err == nil || !strings.Contains(err.Error(), "instance") {
		t.Fatalf("got %v, want an unknown section error", err)
	}
}

func TestDurations(t *testing.T) {
	valid := baseInstance()
	valid.Name, valid.URL, valid.ScaleSet = "a", "https://github.com/org", "s"
	valid.Project, valid.Region, valid.MIG = "p", "r", "m"

	if err := Validate([]Instance{valid}); err != nil {
		t.Fatalf("valid instance refused: %v", err)
	}

	cases := map[string]func(*Instance){
		"reconcile_interval": func(i *Instance) { i.ReconcileInterval = time.Second },
		"boot_timeout":       func(i *Instance) { i.BootTimeout = 0 },
		"above idle_timeout": func(i *Instance) { i.MaxRunnerAge = i.IdleTimeout },
	}
	for want, change := range cases {
		inst := valid
		change(&inst)

		err := Validate([]Instance{inst})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("got %v, want an error about %s", err, want)
		}
	}
}
