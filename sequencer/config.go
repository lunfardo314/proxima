package sequencer

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap/zapcore"
)

func WithName(name string) ConfigOpt {
	return func(o *ConfigOptions) {
		o.SequencerName = name
	}
}

func WithLogLevel(lvl zapcore.Level) ConfigOpt {
	return func(o *ConfigOptions) {
		o.LogLevel = lvl
	}
}

func WithPace(pace int) ConfigOpt {
	util.Assertf(pace >= PaceMinimumTicks, "pace>=PaceMinimumTicks")

	return func(o *ConfigOptions) {
		o.Pace = pace
	}
}

func WithMaxFeeInputs(maxInputs int) ConfigOpt {
	util.Assertf(maxInputs <= 254, "maxInputs<=254")

	return func(o *ConfigOptions) {
		o.MaxFeeInputs = maxInputs
	}
}

func WithMaxTargetTs(ts core.LogicalTime) ConfigOpt {
	return func(o *ConfigOptions) {
		o.MaxTargetTs = ts
	}
}

func WithMaxMilestones(maxMs int) ConfigOpt {
	return func(o *ConfigOptions) {
		o.MaxMilestones = maxMs
	}
}

func WithMaxBranches(maxBranches int) ConfigOpt {
	return func(o *ConfigOptions) {
		o.MaxBranches = maxBranches
	}
}
