package scaler

import "testing"

func TestCounters(t *testing.T) {
	s := newTestScaler(t)

	s.monitor.record(EventJobCompleted, "gh-runner-a", "succeeded")
	s.monitor.record(EventJobCompleted, "gh-runner-b", "failed")
	s.monitor.record(EventJobCompleted, "gh-runner-c", "succeeded")
	s.monitor.record(EventError, "", "free text that must not become a label")
	s.monitor.setPass(PassSummary{})

	counts := s.Counters()
	want := map[Counter]int{
		{Kind: EventJobCompleted, Label: "succeeded"}: 2,
		{Kind: EventJobCompleted, Label: "failed"}:    1,
		{Kind: EventError}:                            1,
		{Kind: EventReconcile}:                        1,
	}

	if len(counts) != len(want) {
		t.Fatalf("got %v, want %v", counts, want)
	}
	for counter, n := range want {
		if counts[counter] != n {
			t.Errorf(
				"%+v: got %d, want %d",
				counter,
				counts[counter],
				n,
			)
		}
	}
}
