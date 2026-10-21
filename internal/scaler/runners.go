package scaler

import (
	"slices"
	"time"
)

// Runner states shown in the API.
const (
	StatePending = "pending" // VM not in the MIG yet
	StateIdle    = "idle"
	StateBusy    = "busy"
)

type runner struct {
	busy    bool
	created time.Time
	// GCE instance ID, it changes when the MIG recreates the VM.
	instanceID uint64
	vmSeen     bool

	job        string
	jobStarted time.Time
}

func (r *runner) state() string {
	switch {
	case r.busy:
		return StateBusy
	case !r.vmSeen:
		return StatePending
	default:
		return StateIdle
	}
}

type RunnerInfo struct {
	Name         string     `json:"name"`
	State        string     `json:"state"`
	CreatedAt    time.Time  `json:"created_at"`
	Job          string     `json:"job,omitempty"`
	JobStartedAt *time.Time `json:"job_started_at,omitempty"`
}

// Runners returns the tracked runners, newest first.
func (s *Scaler) Runners() []RunnerInfo {
	s.mu.Lock()
	infos := make([]RunnerInfo, 0, len(s.runners))
	for name, r := range s.runners {
		info := RunnerInfo{
			Name:      name,
			State:     r.state(),
			CreatedAt: r.created,
			Job:       r.job,
		}
		if !r.jobStarted.IsZero() {
			started := r.jobStarted
			info.JobStartedAt = &started
		}
		infos = append(infos, info)
	}
	s.mu.Unlock()

	slices.SortFunc(infos, func(a, b RunnerInfo) int {
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	return infos
}

func (s *Scaler) idleNewestFirst() []string {
	type candidate struct {
		name    string
		created time.Time
	}

	s.mu.Lock()
	candidates := make([]candidate, 0, len(s.runners))
	for name, r := range s.runners {
		if !r.busy {
			candidates = append(candidates, candidate{name: name, created: r.created})
		}
	}
	s.mu.Unlock()

	slices.SortFunc(candidates, func(a, b candidate) int {
		return b.created.Compare(a.created)
	})

	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.name)
	}
	return names
}

func (s *Scaler) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runners)
}

func (s *Scaler) tracks(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runners[name]
	return ok
}

func (s *Scaler) markBusy(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runners[name]; ok {
		r.busy = true
	}
}

func (s *Scaler) setJob(name, job string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runners[name]; ok {
		r.job = job
		r.jobStarted = time.Now()
	}
}

func (s *Scaler) forget(names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		delete(s.runners, name)
	}
}

func (s *Scaler) runnerCounts() (tracked, busy int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runners {
		if r.busy {
			busy++
		}
	}
	return len(s.runners), busy
}
