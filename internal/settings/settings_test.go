package settings

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func newFlags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.Int("max-runners", 1, "")
	flags.String("name", "", "")
	flags.Duration("idle-timeout", time.Minute, "")
	flags.StringSlice("labels", []string{}, "")
	flags.Bool("yes", false, "")

	if err := flags.Parse(args); err != nil {
		t.Fatal(err)
	}
	return flags
}

func TestPrecedence(t *testing.T) {
	t.Setenv("MIG_SCALER_MAX_RUNNERS", "7")
	t.Setenv("MIG_SCALER_NAME", "from-env")
	t.Setenv("MIG_SCALER_LABELS", "linux, x64")

	r, err := Bind(newFlags(t, "--name", "from-flag"), Options{})
	if err != nil {
		t.Fatal(err)
	}

	if got := r.Int("max-runners"); got != 7 {
		t.Errorf("max-runners: got %d, want 7 from env", got)
	}
	if got := r.String("name"); got != "from-flag" {
		t.Errorf("name: got %q, the flag must win over env", got)
	}
	if got := r.Duration("idle-timeout"); got != time.Minute {
		t.Errorf("idle-timeout: got %v, want the flag default", got)
	}
	if got := r.Strings("labels"); strings.Join(got, "|") != "linux|x64" {
		t.Errorf("labels: got %q, want [linux x64]", got)
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestLabelsFromFlag(t *testing.T) {
	r, err := Bind(newFlags(t, "--labels", "a,b"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Strings("labels"); strings.Join(got, "|") != "a|b" {
		t.Errorf("labels: got %q, want [a b]", got)
	}
}

func TestInvalidValuesAreReported(t *testing.T) {
	t.Setenv("MIG_SCALER_MAX_RUNNERS", "abc")
	t.Setenv("MIG_SCALER_IDLE_TIMEOUT", "soon")

	r, err := Bind(newFlags(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	r.Int("max-runners")
	r.Duration("idle-timeout")

	err = r.Err()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"MIG_SCALER_MAX_RUNNERS", "MIG_SCALER_IDLE_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestSkippedFlagsIgnoreEnv(t *testing.T) {
	t.Setenv("MIG_SCALER_YES", "true")

	flags := newFlags(t)
	r, err := Bind(flags, Options{Skip: []string{"yes"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.String("yes") != "" {
		t.Error("a skipped flag must not be read from env")
	}
	if yes, _ := flags.GetBool("yes"); yes {
		t.Error("a skipped flag must keep its command line value")
	}
}

func TestAliases(t *testing.T) {
	t.Setenv("OLD_NAME", "from-alias")
	opts := Options{Aliases: map[string]string{"name": "OLD_NAME"}}

	r, err := Bind(newFlags(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.String("name"); got != "from-alias" {
		t.Errorf("name: got %q, want the alias value", got)
	}

	t.Setenv("MIG_SCALER_NAME", "from-env")
	r, err = Bind(newFlags(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.String("name"); got != "from-env" {
		t.Errorf("name: got %q, MIG_SCALER_NAME must win over the alias", got)
	}
}
