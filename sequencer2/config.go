package sequencer

import (
	"fmt"
	"math"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/viper"
)

type (
	ConfigOptions struct {
		SequencerName             string
		Pace                      int             // pace in ticks
		MaxTargetTs               base.LedgerTime // for testing
		MaxBranches               int             // for testing
		EnsureSyncedBeforeStart   bool
		DelayStart                time.Duration
		BacklogTagAlongTTLSlots   int
		BacklogDelegationTTLSlots int
		MilestonesTTLSlots        int
		SingleSequencerEnforced   bool
		SeparateLog               bool
		GlobalLogging             bool
		ControllerKeyFile         string // path to keystore file for deferred key loading
	}

	ConfigOption func(options *ConfigOptions)
)

const (
	minimumBacklogTagAlongTTLSlots   = 10
	minimumBacklogDelegationTTLSlots = 20
	minimumMilestonesTTLSlots        = 24 // 10
)

func defaultConfigOptions() *ConfigOptions {
	return &ConfigOptions{
		SequencerName:             "",
		Pace:                      int(ledger.L(base.MaxSlot).TransactionPaceSequencer),
		MaxTargetTs:               base.NilLedgerTime,
		MaxBranches:               math.MaxInt,
		DelayStart:                0, // ledger.SlotDuration(),
		BacklogTagAlongTTLSlots:   minimumBacklogTagAlongTTLSlots,
		BacklogDelegationTTLSlots: minimumBacklogDelegationTTLSlots,
		MilestonesTTLSlots:        minimumMilestonesTTLSlots,
	}
}

func configOptions(opts ...ConfigOption) *ConfigOptions {
	cfg := defaultConfigOptions()
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

func paramsFromConfig() ([]ConfigOption, base.ChainID, error) {
	subViper := viper.Sub("sequencer")
	if subViper == nil {
		return nil, base.ChainID{}, nil
	}
	name := subViper.GetString("name")

	if !subViper.GetBool("enable") {
		// will skip
		return nil, base.ChainID{}, nil
	}
	seqID, err := base.ChainIDFromHexString(subViper.GetString("chain_id"))
	if err != nil {
		return nil, base.ChainID{}, fmt.Errorf("StartFromConfig: can't parse sequencer chain ID: %v", err)
	}

	keyFile := subViper.GetString("controller_key_file")
	if keyFile == "" {
		return nil, base.ChainID{}, fmt.Errorf("StartFromConfig: 'controller_key_file' is required in sequencer config")
	}
	if _, err := os.Stat(keyFile); err != nil {
		return nil, base.ChainID{}, fmt.Errorf("StartFromConfig: controller key file '%s': %v", keyFile, err)
	}

	backlogTagAlongTTLSlots := subViper.GetInt("backlog_tag_along_ttl_slots")
	if backlogTagAlongTTLSlots < minimumBacklogTagAlongTTLSlots {
		backlogTagAlongTTLSlots = minimumBacklogTagAlongTTLSlots
	}
	backlogDelegationTTLSlots := subViper.GetInt("backlog_delegation_ttl_slots")
	if backlogDelegationTTLSlots < minimumBacklogDelegationTTLSlots {
		backlogDelegationTTLSlots = minimumBacklogDelegationTTLSlots
	}
	milestonesTTLSlots := subViper.GetInt("milestones_ttl_slots")
	if milestonesTTLSlots < minimumMilestonesTTLSlots {
		milestonesTTLSlots = minimumMilestonesTTLSlots
	}

	cfg := []ConfigOption{
		WithName(name),
		WithPace(subViper.GetInt("pace")),
		WithMaxBranches(subViper.GetInt("max_branches")),
		WithBacklogTagAlongTTLSlots(backlogTagAlongTTLSlots),
		WithBacklogDelegationTTLSlots(backlogDelegationTTLSlots),
		WithMilestonesTTLSlots(milestonesTTLSlots),
		WithSingleSequencerEnforced,
		WithSeparateLog(subViper.GetBool("logging"), subViper.GetBool("global_logging")),
		WithControllerKeyFile(keyFile),
	}
	if subViper.GetBool("ensure_synced_at_startup") {
		cfg = append(cfg, WithEnsureSyncedAtStartup)
	}
	return cfg, seqID, nil
}

func WithName(name string) ConfigOption {
	return func(o *ConfigOptions) {
		if len(name) > 6 {
			name = name[:6]
		}
		o.SequencerName = name
	}
}

func WithPace(pace int) ConfigOption {
	return func(o *ConfigOptions) {
		lib := ledger.L(base.MaxSlot)
		if pace < int(lib.TransactionPaceSequencer) {
			pace = int(lib.TransactionPaceSequencer)
		}
		o.Pace = pace
	}
}

func WithDelayStart(delay time.Duration) ConfigOption {
	return func(o *ConfigOptions) {
		o.DelayStart = delay
	}
}

func WithMaxBranches(maxBranches int) ConfigOption {
	return func(o *ConfigOptions) {
		if maxBranches >= 1 {
			o.MaxBranches = maxBranches
		}
	}
}

func WithBacklogTagAlongTTLSlots(slots int) ConfigOption {
	return func(o *ConfigOptions) {
		o.BacklogTagAlongTTLSlots = slots
	}
}

func WithBacklogDelegationTTLSlots(slots int) ConfigOption {
	return func(o *ConfigOptions) {
		o.BacklogDelegationTTLSlots = slots
	}
}

func WithMilestonesTTLSlots(slots int) ConfigOption {
	return func(o *ConfigOptions) {
		o.MilestonesTTLSlots = slots
	}
}

func WithEnsureSyncedAtStartup(o *ConfigOptions) {
	o.EnsureSyncedBeforeStart = true
}

func WithSingleSequencerEnforced(o *ConfigOptions) {
	o.SingleSequencerEnforced = true
}

func WithSeparateLog(yesNo, globalLogging bool) ConfigOption {
	return func(o *ConfigOptions) {
		o.SeparateLog = yesNo
		o.GlobalLogging = globalLogging
	}
}

func WithControllerKeyFile(path string) ConfigOption {
	return func(o *ConfigOptions) {
		o.ControllerKeyFile = path
	}
}

func (cfg *ConfigOptions) lines(seqID base.ChainID, controller ledger.SigLock, prefix ...string) *lines.Lines {
	return lines.New(prefix...).
		Add("id: %s", seqID.String()).
		Add("Controller: %s", controller.String()).
		Add("Name: %s", cfg.SequencerName).
		Add("Pace: %d ticks", cfg.Pace).
		Add("MaxTargetTs: %s", cfg.MaxTargetTs.String()).
		Add("MaxSlots: %d", cfg.MaxBranches).
		Add("DelayStart: %v", cfg.DelayStart).
		Add("BacklogTagAlongTTLSlots: %d", cfg.BacklogTagAlongTTLSlots).
		Add("BacklogDelegationTTLSlots: %d", cfg.BacklogDelegationTTLSlots).
		Add("MilestoneTTLSlots: %d", cfg.MilestonesTTLSlots).
		Add("Separate log: %v", cfg.SeparateLog).
		Add("Copy to the global log: %v", cfg.GlobalLogging).
		Add("Controller key file: %s", cfg.ControllerKeyFile)
}
