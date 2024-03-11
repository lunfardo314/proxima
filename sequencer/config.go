package sequencer

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

type (
	ConfigOptions struct {
		SequencerName      string
		Pace               int // pace in ticks
		MaxTagAlongInputs  int
		MaxTargetTs        ledger.Time
		MaxBranches        int
		DelayStart         time.Duration
		BacklogTTLSlots    int
		MilestonesTTLSlots int
		LogAttacherStats   bool
	}

	ConfigOption func(options *ConfigOptions)
)

const (
	DefaultMaxTagAlongInputs  = 20
	MinimumBacklogTTLSlots    = 10
	MinimumMilestonesTTLSlots = 10
)

func defaultConfigOptions() *ConfigOptions {
	return &ConfigOptions{
		SequencerName:      "seq",
		Pace:               ledger.TransactionPaceSequencer(),
		MaxTagAlongInputs:  DefaultMaxTagAlongInputs,
		MaxTargetTs:        ledger.NilLedgerTime,
		MaxBranches:        math.MaxInt,
		DelayStart:         ledger.SlotDuration(),
		BacklogTTLSlots:    MinimumBacklogTTLSlots,
		MilestonesTTLSlots: MinimumMilestonesTTLSlots,
	}
}

func configOptions(opts ...ConfigOption) *ConfigOptions {
	cfg := defaultConfigOptions()
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

func paramsFromConfig(name string) ([]ConfigOption, ledger.ChainID, ed25519.PrivateKey, error) {
	subViper := viper.Sub("sequencers." + name)
	if subViper == nil {
		return nil, ledger.ChainID{}, nil, fmt.Errorf("can't read config")
	}

	if !subViper.GetBool("enable") {
		// will skip
		return nil, ledger.ChainID{}, nil, nil
	}
	seqID, err := ledger.ChainIDFromHexString(subViper.GetString("sequencer_id"))
	if err != nil {
		return nil, ledger.ChainID{}, nil, fmt.Errorf("StartFromConfig: can't parse sequencer ID: %v", err)
	}
	controllerKey, err := util.ED25519PrivateKeyFromHexString(subViper.GetString("controller_key"))
	if err != nil {
		return nil, ledger.ChainID{}, nil, fmt.Errorf("StartFromConfig: can't parse private key: %v", err)
	}
	backlogTTLSlots := subViper.GetInt("backlog_ttl_slots")
	if backlogTTLSlots < MinimumBacklogTTLSlots {
		backlogTTLSlots = MinimumBacklogTTLSlots
	}
	milestonesTTLSlots := subViper.GetInt("milestones_ttl_slots")
	if milestonesTTLSlots < MinimumMilestonesTTLSlots {
		milestonesTTLSlots = MinimumMilestonesTTLSlots
	}

	cfg := []ConfigOption{
		WithName(name),
		WithPace(subViper.GetInt("pace")),
		WithMaxTagAlongInputs(subViper.GetInt("max_tag_along_inputs")),
		WithMaxBranches(subViper.GetInt("max_branches")),
		WithBacklogTTLSlots(backlogTTLSlots),
		WithMilestonesTTLSlots(milestonesTTLSlots),
		WithLogAttacherStats(subViper.GetBool("log_attacher_stats")),
	}
	return cfg, seqID, controllerKey, nil
}

func WithName(name string) ConfigOption {
	return func(o *ConfigOptions) {
		o.SequencerName = name
	}
}

func WithPace(pace int) ConfigOption {
	return func(o *ConfigOptions) {
		if pace < ledger.TransactionPaceSequencer() {
			pace = ledger.TransactionPaceSequencer()
		}
		o.Pace = pace
	}
}

func WithMaxTagAlongInputs(maxInputs int) ConfigOption {
	return func(o *ConfigOptions) {
		if maxInputs >= 1 {
			if maxInputs > 254 {
				o.MaxTagAlongInputs = 254
			} else {
				o.MaxTagAlongInputs = maxInputs
			}
		}
	}
}

func WithMaxBranches(maxBranches int) ConfigOption {
	return func(o *ConfigOptions) {
		if maxBranches >= 1 {
			o.MaxBranches = maxBranches
		}
	}
}

func WithBacklogTTLSlots(slots int) ConfigOption {
	return func(o *ConfigOptions) {
		o.BacklogTTLSlots = slots
	}
}

func WithMilestonesTTLSlots(slots int) ConfigOption {
	return func(o *ConfigOptions) {
		o.MilestonesTTLSlots = slots
	}
}

func WithLogAttacherStats(logAttacherStats bool) ConfigOption {
	return func(o *ConfigOptions) {
		o.LogAttacherStats = logAttacherStats
	}
}
