package api

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ut0mt8/mig-scaleset/internal/scaler"
)

const (
	stateStarting = "starting"
	stateRunning  = "running"
	stateFailed   = "failed"
)

// InstanceInfo is the static description of an instance.
type InstanceInfo struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	ScaleSet string `json:"scale_set"`
	MIG      string `json:"mig"`
	Region   string `json:"region"`
}

// Instance is the server's handle on one instance. Its runner sets the
// scaler and the state; the API and the metrics read them.
type Instance struct {
	info   InstanceInfo
	scaler atomic.Pointer[scaler.Scaler]

	mu        sync.Mutex
	state     string
	lastError string
	since     time.Time
	restarts  int
}

// Starting is called before each attempt. The previous scaler and error
// stay visible until the new attempt runs.
func (i *Instance) Starting() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.setState(stateStarting)
}

func (i *Instance) Running(sc *scaler.Scaler) {
	i.scaler.Store(sc)

	i.mu.Lock()
	defer i.mu.Unlock()
	i.setState(stateRunning)
	i.lastError = ""
}

func (i *Instance) Failed(err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.setState(stateFailed)
	i.lastError = err.Error()
	i.restarts++
}

// setState needs i.mu held.
func (i *Instance) setState(state string) {
	if state != i.state {
		i.since = time.Now()
	}
	i.state = state
}

type instanceSummary struct {
	InstanceInfo
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
	LastError string    `json:"last_error,omitempty"`
	Restarts  int       `json:"restarts"`

	// Only once the scaler runs.
	Waiting *int `json:"waiting,omitempty"`
	Running *int `json:"running,omitempty"`
	Runners *int `json:"runners,omitempty"`
}

func (i *Instance) summary() instanceSummary {
	i.mu.Lock()
	summary := instanceSummary{
		InstanceInfo: i.info,
		State:        i.state,
		Since:        i.since,
		LastError:    i.lastError,
		Restarts:     i.restarts,
	}
	i.mu.Unlock()

	if sc := i.scaler.Load(); sc != nil {
		st := sc.Status()
		summary.Waiting = &st.Queue.Waiting
		summary.Running = &st.Queue.Running
		summary.Runners = &st.Runners.Tracked
	}
	return summary
}
