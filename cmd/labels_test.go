package cmd

import (
	"testing"

	"github.com/actions/scaleset"
)

func TestBuildLabels(t *testing.T) {
	got := labelNames(buildLabels("gcp-runners", []string{" linux", "", "GCP-runners", "x64", "Linux"}))
	want := []string{"gcp-runners", "linux", "x64"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSameLabels(t *testing.T) {
	a := []scaleset.Label{{Name: "gcp-runners"}, {Name: "Linux"}}
	b := []scaleset.Label{{Name: "linux"}, {Name: "gcp-runners"}}
	if !sameLabels(a, b) {
		t.Fatal("expected same labels")
	}
	if sameLabels(a, b[:1]) {
		t.Fatal("expected different labels")
	}
}
