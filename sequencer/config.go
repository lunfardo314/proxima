package sequencer

import (
	"math"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

type (
	ConfigOptions struct {
		SequencerName string
		Pace          int // pace in slots
		MaxFeeInputs  int
		MaxTargetTs   ledger.Time
		MaxMilestones int
		MaxBranches   int
		DelayStart    time.Duration
	}

	ConfigOption func(options *ConfigOptions)
)

const (
	PaceMinimumTicks    = 5
	DefaultMaxFeeInputs = 20
)

//var DefaultDelayOnStart = ledger.SlotDuration()

var DefaultDelayOnStart = time.Duration(0)

func makeConfig(opts ...ConfigOption) *ConfigOptions {
	cfg := defaultConfigOptions()
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

func defaultConfigOptions() *ConfigOptions {
	return &ConfigOptions{
		SequencerName: "seq",
		Pace:          PaceMinimumTicks,
		MaxFeeInputs:  DefaultMaxFeeInputs,
		MaxTargetTs:   ledger.NilLedgerTime,
		MaxMilestones: math.MaxInt,
		MaxBranches:   math.MaxInt,
		DelayStart:    DefaultDelayOnStart,
	}
}

func WithName(name string) ConfigOption {
	return func(o *ConfigOptions) {
		o.SequencerName = name
	}
}

func WithPace(pace int) ConfigOption {
	util.Assertf(pace >= PaceMinimumTicks, "pace>=PaceMinimumTicks")

	return func(o *ConfigOptions) {
		o.Pace = pace
	}
}

func WithMaxFeeInputs(maxInputs int) ConfigOption {
	util.Assertf(maxInputs <= 254, "maxInputs<=254")

	return func(o *ConfigOptions) {
		o.MaxFeeInputs = maxInputs
	}
}

func WithMaxTargetTs(ts ledger.Time) ConfigOption {
	return func(o *ConfigOptions) {
		o.MaxTargetTs = ts
	}
}

func WithMaxMilestones(maxMs int) ConfigOption {
	return func(o *ConfigOptions) {
		o.MaxMilestones = maxMs
	}
}

func WithMaxBranches(maxBranches int) ConfigOption {
	return func(o *ConfigOptions) {
		o.MaxBranches = maxBranches
	}
}

func ReadSequencerConfig() map[string][]string {
	return viper.GetStringMapStringSlice("sequencers")
}
