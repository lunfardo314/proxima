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
		// DoNotWaitForSyncAtStart, when true, lets the sequencer start WITHOUT waiting
		// for the node to be synced. Default false ⇒ the sequencer waits for sync first,
		// so it never builds milestones on a stale / abandoned lineage (which manufactures
		// forks). It MUST be set for a genesis/bootstrap sequencer on an empty network,
		// which can never become "synced" because it is the one creating the chain.
		// Implied by ForceActivity and Standalone (both inherently bootstrap/dev contexts).
		DoNotWaitForSyncAtStart bool
		DelayStart                time.Duration
		BacklogTagAlongTTLSlots   int
		BacklogDelegationTTLSlots int
		MilestonesTTLSlots        int
		MaxTagAlongInputs    int // max tag-along inputs per milestone
		TagAlongDrainRate    int // target tag-alongs to drain per slot
		SingleSequencerEnforced   bool
		SeparateLog               bool
		GlobalLogging             bool
		ControllerKeyFile         string // path to keystore file for deferred key loading
		// ForceActivity when true, the sequencer always issues at least a branch and one
		// milestone per slot regardless of pressure level. Used for bootstrap sequencers
		// that must keep producing to maintain network liveness.
		ForceActivity bool
		// DisableThrottle when true, disables tag-along budget throttling entirely.
		// Budget always stays at full (2/3 of consensus). For tests and debugging.
		DisableThrottle bool
		// Standalone when true, bypasses the libp2p connectivity check before
		// submitting milestones. Intended ONLY for single-node dev networks where
		// there are no peers by design. Never enable on a networked sequencer:
		// it would allow building one-sided forks during a network partition.
		Standalone bool
		// SuppressHealthEnforcement when true, lets this sequencer issue branch
		// transactions whose coverage delta is below the health threshold,
		// suppressing the node-level issue-gates (proposer pre-build gate and the
		// submit gate). Read from the node-global top-level 'suppress_health_enforcement'
		// config key — the SAME flag the workflow attacher reads to accept unhealthy
		// branches. Intended for restarting a network from an old snapshot, where
		// frozen-coverage expiry can otherwise make a healthy branch impossible to
		// reconstruct (deadlock). Health remains a consensus signal via LRB selection.
		SuppressHealthEnforcement bool
		// SuppressCoverageContributionLowerBound when true, lets this sequencer issue branch
		// transactions whose sequencer coverage is below the per-sequencer lower
		// bound. Read from the node-global top-level 'suppress_coverage_contribution_lower_bound'
		// config key — the SAME flag the workflow attacher reads. The bound constant
		// stays on the ledger; enforcement is suppressible for the same snapshot-restart
		// deadlock reason as SuppressHealthEnforcement (expired frozen coverage). The
		// upper bound remains a ledger constraint.
		SuppressCoverageContributionLowerBound bool
	}

	ConfigOption func(options *ConfigOptions)
)

const (
	minimumBacklogTagAlongTTLSlots   = 10
	minimumBacklogDelegationTTLSlots = 20
	minimumMilestonesTTLSlots        = 24 // 10
	defaultMaxTagAlongInputs         = 15
	defaultTagAlongDrainRate         = 100 // ~10 TPS per sequencer with 1.024s slots
)

func defaultConfigOptions() *ConfigOptions {
	return &ConfigOptions{
		SequencerName:             "",
		Pace:                      int(ledger.L(base.MaxSlot).TransactionPaceSequencer),
		MaxTargetTs:               base.NilLedgerTime,
		MaxBranches:               math.MaxInt,
		DelayStart:                ledger.SlotDuration(), // to fill up the tippool
		BacklogTagAlongTTLSlots:   minimumBacklogTagAlongTTLSlots,
		BacklogDelegationTTLSlots: minimumBacklogDelegationTTLSlots,
		MilestonesTTLSlots:        minimumMilestonesTTLSlots,
		MaxTagAlongInputs:    defaultMaxTagAlongInputs,
		TagAlongDrainRate:    defaultTagAlongDrainRate,
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
		WithMaxTagAlongInputs(subViper.GetInt("max_tag_along_inputs")),
		WithTagAlongDrainRate(subViper.GetInt("tag_along_drain_rate")),
		WithSingleSequencerEnforced,
		WithSeparateLog(subViper.GetBool("logging"), subViper.GetBool("global_logging")),
		WithControllerKeyFile(keyFile),
	}
	if subViper.GetBool("do_not_wait_for_sync_at_start") {
		cfg = append(cfg, WithDoNotWaitForSync)
	}
	if subViper.GetBool("force_activity") {
		cfg = append(cfg, WithForceActivity)
	}
	if subViper.GetBool("disable_throttle") {
		cfg = append(cfg, WithDisableThrottle)
	}
	if subViper.GetBool("standalone") {
		cfg = append(cfg, WithStandalone)
	}
	// node-global flags (top-level keys), shared with the workflow attacher
	if viper.GetBool("suppress_health_enforcement") {
		cfg = append(cfg, WithSuppressHealthEnforcement)
	}
	if viper.GetBool("suppress_coverage_contribution_lower_bound") {
		cfg = append(cfg, WithSuppressCoverageContributionLowerBound)
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

func WithMaxTagAlongInputs(n int) ConfigOption {
	return func(o *ConfigOptions) {
		if n >= 1 {
			o.MaxTagAlongInputs = n
		}
	}
}

func WithTagAlongDrainRate(rate int) ConfigOption {
	return func(o *ConfigOptions) {
		if rate >= 1 {
			o.TagAlongDrainRate = rate
		}
	}
}

func WithDoNotWaitForSync(o *ConfigOptions) {
	o.DoNotWaitForSyncAtStart = true
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

func WithForceActivity(o *ConfigOptions) {
	o.ForceActivity = true
}

func WithDisableThrottle(o *ConfigOptions) {
	o.DisableThrottle = true
}

func WithStandalone(o *ConfigOptions) {
	o.Standalone = true
}

func WithSuppressHealthEnforcement(o *ConfigOptions) {
	o.SuppressHealthEnforcement = true
}

func WithSuppressCoverageContributionLowerBound(o *ConfigOptions) {
	o.SuppressCoverageContributionLowerBound = true
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
		Add("MaxTagAlongInputs: %d", cfg.MaxTagAlongInputs).
		Add("TagAlongDrainRate: %d/slot", cfg.TagAlongDrainRate).
		Add("Separate log: %v", cfg.SeparateLog).
		Add("Copy to the global log: %v", cfg.GlobalLogging).
		Add("Controller key file: %s", cfg.ControllerKeyFile).
		Add("Force activity: %v", cfg.ForceActivity).
		Add("Disable throttle: %v", cfg.DisableThrottle).
		Add("Standalone: %v", cfg.Standalone).
		Add("Suppress health enforcement: %v", cfg.SuppressHealthEnforcement).
		Add("Suppress coverage lower bound: %v", cfg.SuppressCoverageContributionLowerBound)
}
