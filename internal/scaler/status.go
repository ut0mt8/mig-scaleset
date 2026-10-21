package scaler

import "time"

// Queue is GitHub's view of the jobs for this scale set.
type Queue struct {
	Waiting   int `json:"waiting"` // assigned but not running yet
	Available int `json:"available"`
	Acquired  int `json:"acquired"`
	Assigned  int `json:"assigned"`
	Running   int `json:"running"`
}

type RunnerCounts struct {
	Target  int `json:"target"`
	Tracked int `json:"tracked"`
	Pending int `json:"pending"`
	Idle    int `json:"idle"`
	Busy    int `json:"busy"`
	Min     int `json:"min"`
	Max     int `json:"max"`
}

type GitHubCounts struct {
	Registered int `json:"registered"`
	Busy       int `json:"busy"`
	Idle       int `json:"idle"`
	// Registered but not connected: booting, broken, or a leftover.
	Offline   int        `json:"offline"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type PassSummary struct {
	At              time.Time `json:"at"`
	TookMS          int64     `json:"took_ms"`
	VMsInMIG        int       `json:"vms_in_mig"`
	Adopted         []string  `json:"adopted"`
	Pending         []string  `json:"pending"`
	Forgotten       []string  `json:"forgotten"`
	DeletedDead     []string  `json:"deleted_dead"`
	DeletedFinished []string  `json:"deleted_finished"`
	ForeignVMs      []string  `json:"foreign_vms"`
}

type Status struct {
	ScaleSetID    int          `json:"scale_set_id"`
	MIG           string       `json:"mig"`
	Region        string       `json:"region"`
	Queue         Queue        `json:"queue"`
	Runners       RunnerCounts `json:"runners"`
	GitHub        GitHubCounts `json:"github"`
	LastReconcile *PassSummary `json:"last_reconcile,omitempty"`
}

func (s *Scaler) Status() Status {
	status := Status{
		ScaleSetID: s.cfg.ScaleSetID,
		MIG:        s.cfg.MIG,
		Region:     s.cfg.Region,
		Runners: RunnerCounts{
			Min: s.cfg.MinRunners,
			Max: s.cfg.MaxRunners,
		},
	}

	for _, r := range s.Runners() {
		status.Runners.Tracked++
		switch r.State {
		case StatePending:
			status.Runners.Pending++
		case StateIdle:
			status.Runners.Idle++
		case StateBusy:
			status.Runners.Busy++
		}
	}

	s.monitor.mu.Lock()
	defer s.monitor.mu.Unlock()

	status.LastReconcile = s.monitor.lastPass
	status.Runners.Target = s.cfg.MinRunners

	stats := s.monitor.stats
	if stats == nil {
		return status
	}

	status.Queue = Queue{
		Waiting:   max(0, stats.TotalAssignedJobs-stats.TotalRunningJobs),
		Available: stats.TotalAvailableJobs,
		Acquired:  stats.TotalAcquiredJobs,
		Assigned:  stats.TotalAssignedJobs,
		Running:   stats.TotalRunningJobs,
	}
	status.Runners.Target = min(s.cfg.MaxRunners, s.cfg.MinRunners+stats.TotalAssignedJobs)

	updatedAt := s.monitor.statsAt
	status.GitHub = GitHubCounts{
		Registered: stats.TotalRegisteredRunners,
		Busy:       stats.TotalBusyRunners,
		Idle:       stats.TotalIdleRunners,
		Offline:    max(0, stats.TotalRegisteredRunners-stats.TotalBusyRunners-stats.TotalIdleRunners),
		UpdatedAt:  &updatedAt,
	}
	return status
}
