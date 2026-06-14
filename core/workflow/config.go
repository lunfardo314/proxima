package workflow

import (
	"fmt"
	"runtime"

	"go.uber.org/zap"
)

type (
	ConfigParams struct {
		disableMemDAGGC            bool
		maxConcurrentAttachers     int
		suppressHealthEnforcement  bool
		suppressCoverageContributionLowerBound bool
	}

	ConfigOption func(c *ConfigParams)
)

const (
	// minMaxConcurrentAttachers is the floor for the attacher cap (also the value on
	// boxes with <= 2 GOMAXPROCS). Keeps small nodes at the historical default of 20.
	minMaxConcurrentAttachers = 20
	// attachersPerCPU scales the cap with available parallelism. Attachers are mixed
	// I/O+CPU work (peer pulls, DB reads, constraint validation), so oversubscribing
	// past the core count keeps cores busy while other attachers wait on I/O.
	attachersPerCPU = 10
)

// defaultMaxConcurrentAttachers = max(minMaxConcurrentAttachers, attachersPerCPU*GOMAXPROCS).
// Uses GOMAXPROCS (cgroup/quota aware) rather than NumCPU (physical cores), so a
// CPU-limited container does not over-subscribe.
func defaultMaxConcurrentAttachers() int {
	if n := attachersPerCPU * runtime.GOMAXPROCS(0); n > minMaxConcurrentAttachers {
		return n
	}
	return minMaxConcurrentAttachers
}

func defaultConfigParams() ConfigParams {
	return ConfigParams{
		maxConcurrentAttachers: defaultMaxConcurrentAttachers(),
	}
}

// OptionDisableMemDAGGC used for testing, to disable pruner
// Config key: 'workflow.do_not_start_pruner: true'
func OptionDisableMemDAGGC(c *ConfigParams) {
	c.disableMemDAGGC = true
}

// OptionMaxConcurrentAttachers overrides the attacher cap. Applied from the
// 'workflow.max_concurrent_attachers' config key (when > 0) and in tests.
func OptionMaxConcurrentAttachers(n int) ConfigOption {
	return func(c *ConfigParams) {
		c.maxConcurrentAttachers = n
	}
}

// OptionSuppressHealthEnforcement disables the attacher's rejection of unhealthy
// branch transactions. Applied from the top-level 'suppress_health_enforcement'
// config key. Branch health is enforced in Go (not on the immutable ledger);
// suppressing it node-wide is for coordinated restart from an old snapshot where
// frozen-coverage expiry would otherwise deadlock branch issuance.
func OptionSuppressHealthEnforcement(c *ConfigParams) {
	c.suppressHealthEnforcement = true
}

// OptionSuppressCoverageContributionLowerBound disables the attacher's rejection of branches
// whose sequencer coverage is below the per-sequencer lower bound. Applied from the
// top-level 'suppress_coverage_contribution_lower_bound' config key. The bound constant stays on
// the ledger; suppressing enforcement node-wide is for restart from an old snapshot
// where frozen-coverage expiry would otherwise leave sequencers stuck below it.
func OptionSuppressCoverageContributionLowerBound(c *ConfigParams) {
	c.suppressCoverageContributionLowerBound = true
}

func (cfg *ConfigParams) log(log *zap.SugaredLogger) {
	if cfg.disableMemDAGGC {
		log.Info("[workflow config] do not start pruner")
	}
	if cfg.suppressHealthEnforcement {
		log.Warn("[workflow config] branch health enforcement SUPPRESSED (suppress_health_enforcement=true): unhealthy branches will be accepted")
	}
	if cfg.suppressCoverageContributionLowerBound {
		log.Warn("[workflow config] sequencer coverage lower-bound enforcement SUPPRESSED (suppress_coverage_contribution_lower_bound=true): below-bound branches will be accepted")
	}
	note := ""
	if auto := defaultMaxConcurrentAttachers(); cfg.maxConcurrentAttachers != auto {
		note = fmt.Sprintf(" (config override; auto = %d)", auto)
	}
	log.Infof("[workflow config] max concurrent attachers: %d [%d per core x GOMAXPROCS=%d, floor %d]%s",
		cfg.maxConcurrentAttachers, attachersPerCPU, runtime.GOMAXPROCS(0), minMaxConcurrentAttachers, note)
}
