package sequencer

import (
	"slices"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/utangle_old"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
	"go.uber.org/zap/zapcore"
)

type (
	ConfigOptions struct {
		SequencerName string
		Pace          int // pace in slots
		LogLevel      zapcore.Level
		LogOutputs    []string
		TraceTippool  bool
		LogTimeLayout string
		MaxFeeInputs  int
		MaxTargetTs   core.LogicalTime
		MaxMilestones int
		MaxBranches   int
		StartOutput   utangle_old.WrappedOutput
	}

	ConfigOpt func(options *ConfigOptions)
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

func WithLogOutput(logOutput string) ConfigOpt {
	return func(o *ConfigOptions) {
		if logOutput != "" && slices.Index(o.LogOutputs, logOutput) < 0 {
			o.LogOutputs = append(o.LogOutputs, logOutput)
		}
	}
}

func WithLogTimeLayout(logTimeLayout string) ConfigOpt {
	return func(o *ConfigOptions) {
		o.LogTimeLayout = logTimeLayout
	}
}

func WithTraceTippool(trace bool) ConfigOpt {
	return func(o *ConfigOptions) {
		o.TraceTippool = trace
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

func WithStartOutput(wOut utangle_old.WrappedOutput) ConfigOpt {
	return func(o *ConfigOptions) {
		o.StartOutput = wOut
	}
}

func ReadSequencerConfig() map[string][]string {
	return viper.GetStringMapStringSlice("sequencers")
}
