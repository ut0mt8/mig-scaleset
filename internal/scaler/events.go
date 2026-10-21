package scaler

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/actions/scaleset"
)

const maxEvents = 200

// Event kinds. The api package turns them into counters.
const (
	EventRegistered   = "registered"
	EventDeregistered = "deregistered"
	EventJobStarted   = "job_started"
	EventJobCompleted = "job_completed"
	EventScaleUp      = "scale_up"
	EventScaleDown    = "scale_down"
	EventVMDeleted    = "vm_deleted"
	EventAdopted      = "adopted"
	EventError        = "error"
	// Counted, not listed.
	EventReconcile = "reconcile"
)

// Kinds whose detail is a small fixed set, kept as a counter label.
var labelledKinds = map[string]bool{
	EventJobCompleted: true,
	EventDeregistered: true,
	EventVMDeleted:    true,
}

type Event struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	Runner string    `json:"runner,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Counter is an event kind and, for labelled kinds, its detail.
type Counter struct {
	Kind  string
	Label string
}

// monitor keeps what the API shows besides the runners.
type monitor struct {
	mu       sync.Mutex
	stats    *scaleset.RunnerScaleSetStatistic
	statsAt  time.Time
	lastPass *PassSummary
	events   []Event
	counts   map[Counter]int
}

func newMonitor() monitor {
	return monitor{
		events: []Event{},
		counts: map[Counter]int{},
	}
}

func (m *monitor) record(kind, runner, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.events = append(m.events, Event{
		Time:   time.Now(),
		Kind:   kind,
		Runner: runner,
		Detail: detail,
	})
	if len(m.events) > maxEvents {
		m.events = slices.Clone(m.events[len(m.events)-maxEvents:])
	}

	counter := Counter{Kind: kind}
	if labelledKinds[kind] {
		counter.Label = detail
	}
	m.counts[counter]++
}

func (m *monitor) setStats(stats *scaleset.RunnerScaleSetStatistic) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stats = stats
	m.statsAt = time.Now()
}

func (m *monitor) setPass(p PassSummary) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPass = &p
	m.counts[Counter{Kind: EventReconcile}]++
}

// Counters returns how many events of each kind happened since start.
func (s *Scaler) Counters() map[Counter]int {
	s.monitor.mu.Lock()
	defer s.monitor.mu.Unlock()
	return maps.Clone(s.monitor.counts)
}

// Events returns the most recent events, newest first.
func (s *Scaler) Events(limit int) []Event {
	s.monitor.mu.Lock()
	defer s.monitor.mu.Unlock()

	// Never nil, so the JSON is [] and not null.
	events := make([]Event, 0, len(s.monitor.events))
	events = append(events, s.monitor.events...)
	slices.Reverse(events)
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events
}
