package workflow

import (
	"fmt"
	"runtime"

	"go.uber.org/zap"
)

type (
	ConfigParams struct {
		disableMemDAGGC        bool
		maxConcurrentAttachers int
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

func (cfg *ConfigParams) log(log *zap.SugaredLogger) {
	if cfg.disableMemDAGGC {
		log.Info("[workflow config] do not start pruner")
	}
	note := ""
	if auto := defaultMaxConcurrentAttachers(); cfg.maxConcurrentAttachers != auto {
		note = fmt.Sprintf(" (config override; auto = %d)", auto)
	}
	log.Infof("[workflow config] max concurrent attachers: %d [%d per core x GOMAXPROCS=%d, floor %d]%s",
		cfg.maxConcurrentAttachers, attachersPerCPU, runtime.GOMAXPROCS(0), minMaxConcurrentAttachers, note)
}
