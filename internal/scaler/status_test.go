package scaler

import (
	"encoding/json"
	"testing"

	compute "cloud.google.com/go/compute/apiv1"
	"github.com/actions/scaleset"
)

func newTestScaler(t *testing.T) *Scaler {
	t.Helper()

	s, err := New(Config{
		GitHub:     &scaleset.Client{},
		Compute:    &compute.RegionInstanceGroupManagersClient{},
		Prefix:     "gh-runner",
		MaxRunners: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The UI calls .map() on these, null breaks it.
func TestEmptyListsAreJSONArrays(t *testing.T) {
	s := newTestScaler(t)

	for name, value := range map[string]any{
		"events":  s.Events(50),
		"runners": s.Runners(),
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "[]" {
			t.Errorf("%s: got %s, want []", name, raw)
		}
	}
}
