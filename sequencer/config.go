package sequencer

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/viper"
)

type (
	ConfigOptions struct {
		SequencerName           string
		Pace                    int // pace in ticks
		MaxTagAlongInputs       int
		MaxTargetTs             ledger.Time
		MaxBranches             int
		DelayStart              time.Duration
		BacklogTTLSlots         int
		MilestonesTTLSlots      int
		SingleSequencerEnforced bool
	}

	ConfigOption func(options *ConfigOptions)
)

const (
	DefaultMaxTagAlongInputs  = 20
	MinimumBacklogTTLSlots    = 10
	MinimumMilestonesTTLSlots = 24 // 10
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

func paramsFromConfig() ([]ConfigOption, ledger.ChainID, ed25519.PrivateKey, error) {
	subViper := viper.Sub("sequencer")
	if subViper == nil {
		return nil, ledger.ChainID{}, nil, nil
	}
	name := subViper.GetString("name")
	if name == "" {
		return nil, ledger.ChainID{}, nil, fmt.Errorf("StartFromConfig: sequencer must have a name")
	}

	if !subViper.GetBool("enable") {
		// will skip
		return nil, ledger.ChainID{}, nil, nil
	}
	seqID, err := ledger.ChainIDFromHexString(subViper.GetString("chain_id"))
	if err != nil {
		return nil, ledger.ChainID{}, nil, fmt.Errorf("StartFromConfig: can't parse sequencer chain ID: %v", err)
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
		WithSingleSequencerEnforced,
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

func WithDelayStart(delay time.Duration) ConfigOption {
	return func(o *ConfigOptions) {
		o.DelayStart = delay
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

func WithSingleSequencerEnforced(o *ConfigOptions) {
	o.SingleSequencerEnforced = true
}

func (cfg *ConfigOptions) lines(seqID ledger.ChainID, controller ledger.AddressED25519, prefix ...string) *lines.Lines {
	return lines.New(prefix...).
		Add("ID: %s", seqID.String()).
		Add("Controller: %s", controller.String()).
		Add("Name: %s", cfg.SequencerName).
		Add("Pace: %d ticks", cfg.Pace).
		Add("MaxTagAlongInputs: %d", cfg.MaxTagAlongInputs).
		Add("MaxTargetTs: %s", cfg.MaxTargetTs.String()).
		Add("MaxSlots: %d", cfg.MaxBranches).
		Add("DelayStart: %v", cfg.DelayStart).
		Add("BacklogTTLSlots: %d", cfg.BacklogTTLSlots).
		Add("MilestoneTTLSlots: %d", cfg.MilestonesTTLSlots)
}
