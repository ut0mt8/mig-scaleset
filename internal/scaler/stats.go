package scaler

import (
	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"go.uber.org/zap"
)

// StatsLogger logs the scale set statistics GitHub sends with every message,
// next to the scaler's own counts.
type StatsLogger struct {
	scaler *Scaler
	// -1 until the first message.
	lastRegistered int
}

var _ listener.MetricsRecorder = (*StatsLogger)(nil)

func NewStatsLogger(s *Scaler) *StatsLogger {
	return &StatsLogger{scaler: s, lastRegistered: -1}
}

func (l *StatsLogger) RecordStatistics(stats *scaleset.RunnerScaleSetStatistic) {
	if stats == nil {
		return
	}

	log := l.scaler.log
	cfg := l.scaler.cfg

	// GitHub sends no event when a runner registers or leaves, but this
	// count moves.
	registered := stats.TotalRegisteredRunners
	if l.lastRegistered >= 0 && registered != l.lastRegistered {
		log.Info(
			"GitHub registered runners changed",
			zap.Int("from", l.lastRegistered),
			zap.Int("to", registered),
			zap.Int("delta", registered-l.lastRegistered),
		)
	}
	l.lastRegistered = registered
	l.scaler.monitor.setStats(stats)

	tracked, busy := l.scaler.runnerCounts()
	log.Info(
		"message stats",
		zap.Int("assigned_jobs", stats.TotalAssignedJobs),
		zap.Int("available_jobs", stats.TotalAvailableJobs),
		zap.Int("acquired_jobs", stats.TotalAcquiredJobs),
		zap.Int("running_jobs", stats.TotalRunningJobs),
		zap.Int("gh_registered_runners", registered),
		zap.Int("gh_busy_runners", stats.TotalBusyRunners),
		zap.Int("gh_idle_runners", stats.TotalIdleRunners),
		zap.Int("target", min(cfg.MaxRunners, cfg.MinRunners+stats.TotalAssignedJobs)),
		zap.Int("tracked", tracked),
		zap.Int("busy", busy),
	)
}

func (*StatsLogger) RecordJobStarted(*scaleset.JobStarted)     {}
func (*StatsLogger) RecordJobCompleted(*scaleset.JobCompleted) {}
func (*StatsLogger) RecordDesiredRunners(int)                  {}
