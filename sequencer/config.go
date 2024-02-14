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
		SequencerName     string
		Pace              int // pace in ticks
		MaxTagAlongInputs int
		MaxTargetTs       ledger.Time
		MaxMilestones     int
		MaxBranches       int
		DelayStart        time.Duration
	}

	ConfigOption func(options *ConfigOptions)
)

const (
	DefaultMaxTagAlongInputs = 20
)

func defaultConfigOptions() *ConfigOptions {
	return &ConfigOptions{
		SequencerName:     "seq",
		Pace:              ledger.TransactionPaceSequencer(),
		MaxTagAlongInputs: DefaultMaxTagAlongInputs,
		MaxTargetTs:       ledger.NilLedgerTime,
		MaxMilestones:     math.MaxInt,
		MaxBranches:       math.MaxInt,
		DelayStart:        ledger.SlotDuration(),
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
	cfg := []ConfigOption{
		WithName(name),
		WithPace(subViper.GetInt("pace")),
		WithMaxTagAlongInputs(subViper.GetInt("max_tag_along_inputs")),
		WithMaxBranches(subViper.GetInt("max_branches")),
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
		if maxInputs < 1 {
			o.MaxTagAlongInputs = 1
		} else {
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
		o.MaxBranches = maxBranches
	}
}
