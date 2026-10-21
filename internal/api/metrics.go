package api

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ut0mt8/mig-scaleset/internal/scaler"
)

const namespace = "mig_scaler"

// desc builds a metric description; every metric has an instance label.
func desc(name, help string, labels ...string) *prometheus.Desc {
	labels = append([]string{"instance"}, labels...)
	return prometheus.NewDesc(
		namespace+"_"+name,
		help,
		labels,
		nil,
	)
}

type counterMetric struct {
	desc *prometheus.Desc
	// The event detail is the metric's second label.
	labelled bool
}

var counterMetrics = map[string]counterMetric{
	scaler.EventRegistered: {
		desc: desc("runners_registered_total", "Runners registered on GitHub."),
	},
	scaler.EventDeregistered: {
		desc:     desc("runners_deregistered_total", "Runners removed from GitHub.", "reason"),
		labelled: true,
	},
	scaler.EventJobStarted: {
		desc: desc("jobs_started_total", "Jobs started on a runner."),
	},
	scaler.EventJobCompleted: {
		desc:     desc("jobs_completed_total", "Jobs finished, by result.", "result"),
		labelled: true,
	},
	scaler.EventScaleUp: {
		desc: desc("scale_ups_total", "Scale up operations."),
	},
	scaler.EventScaleDown: {
		desc: desc("scale_downs_total", "Scale down operations."),
	},
	scaler.EventVMDeleted: {
		desc:     desc("vms_deleted_total", "Runner VMs deleted.", "reason"),
		labelled: true,
	},
	scaler.EventAdopted: {
		desc: desc("vms_adopted_total", "VMs adopted from a previous run."),
	},
	scaler.EventError: {
		desc: desc("errors_total", "Failed VM creations, MIG errors and failed reconciles."),
	},
	scaler.EventReconcile: {
		desc: desc("reconcile_passes_total", "Completed reconcile passes."),
	},
}

var (
	infoDesc = desc(
		"info",
		"Instance information, always 1.",
		"version",
		"url",
		"scale_set",
		"scale_set_id",
		"mig",
		"region",
	)
	readyDesc          = desc("ready", "1 while the instance is running, 0 while starting or failed.")
	restartsDesc       = desc("instance_restarts_total", "Times the instance failed and was restarted.")
	jobsDesc           = desc("jobs", "Jobs of the scale set on GitHub, by state.", "state")
	runnersDesc        = desc("runners", "Runners tracked by the scaler, by state.", "state")
	runnersTargetDesc  = desc("runners_target", "Runners the scaler aims for.")
	runnersMinDesc     = desc("runners_min", "Configured minimum runners.")
	runnersMaxDesc     = desc("runners_max", "Configured maximum runners.")
	githubRunnersDesc  = desc("github_runners", "Runners as GitHub sees them, by state.", "state")
	githubStatsAgeDesc = desc("github_stats_age_seconds", "Age of the last statistics from GitHub.")
	reconcileTimeDesc  = desc("reconcile_last_timestamp_seconds", "Time of the last reconcile pass.")
	reconcileTookDesc  = desc("reconcile_last_duration_seconds", "Duration of the last reconcile pass.")
	reconcileVMsDesc   = desc("reconcile_vms", "VMs seen by the last reconcile pass.", "kind")
)

// collector reads every instance at scrape time, so the metrics are always
// the same numbers as the status page.
type collector struct {
	server *Server
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		infoDesc,
		readyDesc,
		restartsDesc,
		jobsDesc,
		runnersDesc,
		runnersTargetDesc,
		runnersMinDesc,
		runnersMaxDesc,
		githubRunnersDesc,
		githubStatsAgeDesc,
		reconcileTimeDesc,
		reconcileTookDesc,
		reconcileVMsDesc,
	} {
		ch <- d
	}
	for _, metric := range counterMetrics {
		ch <- metric.desc
	}
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	for _, inst := range c.server.instances {
		c.collectInstance(sink{ch: ch, instance: inst.info.Name}, inst)
	}
}

// sink sends the metrics of one instance, adding its label.
type sink struct {
	ch       chan<- prometheus.Metric
	instance string
}

func (s sink) gauge(d *prometheus.Desc, value float64, labels ...string) {
	s.ch <- prometheus.MustNewConstMetric(
		d,
		prometheus.GaugeValue,
		value,
		s.labels(labels)...,
	)
}

func (s sink) counter(d *prometheus.Desc, value float64, labels ...string) {
	s.ch <- prometheus.MustNewConstMetric(
		d,
		prometheus.CounterValue,
		value,
		s.labels(labels)...,
	)
}

func (s sink) labels(extra []string) []string {
	return append([]string{s.instance}, extra...)
}

func (c *collector) collectInstance(out sink, inst *Instance) {
	summary := inst.summary()

	ready := 0.0
	if summary.State == stateRunning {
		ready = 1
	}
	out.gauge(readyDesc, ready)
	out.counter(restartsDesc, float64(summary.Restarts))

	sc := inst.scaler.Load()
	if sc == nil {
		return
	}

	st := sc.Status()
	out.gauge(
		infoDesc,
		1,
		c.server.version,
		inst.info.URL,
		inst.info.ScaleSet,
		strconv.Itoa(st.ScaleSetID),
		st.MIG,
		st.Region,
	)

	collectStatus(out, st)
	collectCounters(out, sc.Counters())
}

func collectStatus(out sink, st scaler.Status) {
	jobs := map[string]int{
		"waiting":   st.Queue.Waiting,
		"available": st.Queue.Available,
		"acquired":  st.Queue.Acquired,
		"assigned":  st.Queue.Assigned,
		"running":   st.Queue.Running,
	}
	for state, n := range jobs {
		out.gauge(jobsDesc, float64(n), state)
	}

	runners := map[string]int{
		scaler.StatePending: st.Runners.Pending,
		scaler.StateIdle:    st.Runners.Idle,
		scaler.StateBusy:    st.Runners.Busy,
	}
	for state, n := range runners {
		out.gauge(runnersDesc, float64(n), state)
	}
	out.gauge(runnersTargetDesc, float64(st.Runners.Target))
	out.gauge(runnersMinDesc, float64(st.Runners.Min))
	out.gauge(runnersMaxDesc, float64(st.Runners.Max))

	github := map[string]int{
		"registered": st.GitHub.Registered,
		"busy":       st.GitHub.Busy,
		"idle":       st.GitHub.Idle,
		"offline":    st.GitHub.Offline,
	}
	for state, n := range github {
		out.gauge(githubRunnersDesc, float64(n), state)
	}
	if st.GitHub.UpdatedAt != nil {
		out.gauge(githubStatsAgeDesc, time.Since(*st.GitHub.UpdatedAt).Seconds())
	}

	pass := st.LastReconcile
	if pass == nil {
		return
	}
	out.gauge(reconcileTimeDesc, float64(pass.At.Unix()))
	out.gauge(reconcileTookDesc, float64(pass.TookMS)/1000)

	vms := map[string]int{
		"in_mig":  pass.VMsInMIG,
		"pending": len(pass.Pending),
		"foreign": len(pass.ForeignVMs),
	}
	for kind, n := range vms {
		out.gauge(reconcileVMsDesc, float64(n), kind)
	}
}

func collectCounters(out sink, counts map[scaler.Counter]int) {
	for counter, n := range counts {
		metric, ok := counterMetrics[counter.Kind]
		if !ok {
			continue
		}

		labels := []string{}
		if metric.labelled {
			labels = append(labels, counter.Label)
		}
		out.counter(metric.desc, float64(n), labels...)
	}
}
