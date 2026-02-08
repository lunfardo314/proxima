package sequencer

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/viper"
	"golang.org/x/term"
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
		SequencerName:             "seq",
		Pace:                      int(ledger.L(base.MaxSlot).TransactionPaceSequencer),
		MaxTargetTs:               base.NilLedgerTime,
		MaxBranches:               math.MaxInt,
		DelayStart:                ledger.SlotDuration(),
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

func paramsFromConfig() ([]ConfigOption, base.ChainID, ed25519.PrivateKey, error) {
	subViper := viper.Sub("sequencer")
	if subViper == nil {
		return nil, base.ChainID{}, nil, nil
	}
	name := subViper.GetString("name")
	if name == "" {
		return nil, base.ChainID{}, nil, fmt.Errorf("StartFromConfig: sequencer must have a name")
	}

	if !subViper.GetBool("enable") {
		// will skip
		return nil, base.ChainID{}, nil, nil
	}
	seqID, err := base.ChainIDFromHexString(subViper.GetString("chain_id"))
	if err != nil {
		return nil, base.ChainID{}, nil, fmt.Errorf("StartFromConfig: can't parse sequencer chain ID: %v", err)
	}
	controllerKey, err := loadControllerKey(subViper)
	if err != nil {
		return nil, base.ChainID{}, nil, fmt.Errorf("StartFromConfig: can't load controller key: %v", err)
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
	}
	if subViper.GetBool("ensure_synced_at_startup") {
		cfg = append(cfg, WithEnsureSyncedAtStartup)
	}
	return cfg, seqID, controllerKey, nil
}

// loadControllerKey reads the sequencer controller private key from a .key file (JSON keystore).
// The file is specified by the 'controller_key_file' config field.
// Both encrypted and unencrypted keystore formats are supported.
func loadControllerKey(subViper *viper.Viper) (ed25519.PrivateKey, error) {
	keyFile := subViper.GetString("controller_key_file")
	if keyFile == "" {
		return nil, fmt.Errorf("no controller key: set 'controller_key_file' in sequencer config")
	}
	return loadFromKeystore(keyFile)
}

// loadFromKeystore loads a private key from a JSON keystore file.
// Supports both encrypted and unencrypted keystores.
// For encrypted keystores, checks PROXIMA_KEY_PASSPHRASE env var first, then prompts on stdin.
func loadFromKeystore(path string) (ed25519.PrivateKey, error) {
	ks, err := keystore.LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	if ks.KeyType != keystore.KeyTypeED25519 {
		return nil, fmt.Errorf("unsupported key type %d in keystore '%s'", ks.KeyType, path)
	}

	passphrase := ""
	if ks.IsEncrypted() {
		passphrase = os.Getenv("PROXIMA_KEY_PASSPHRASE")
		if passphrase == "" {
			hint := ""
			if ks.Hint != "" {
				hint = fmt.Sprintf(" (hint: %s)", ks.Hint)
			}
			fmt.Printf("Enter passphrase for '%s'%s: ", path, hint)
			passBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
			if err != nil {
				return nil, fmt.Errorf("failed to read passphrase: %v", err)
			}
			fmt.Println()
			passphrase = string(passBytes)
		}
	}

	keyBytes, err := ks.GetPrivateKey(passphrase)
	if err != nil {
		return nil, err
	}
	if len(keyBytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("key has wrong size: %d (expected %d)", len(keyBytes), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(keyBytes), nil
}

func WithName(name string) ConfigOption {
	return func(o *ConfigOptions) {
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
		Add("Copy to the global log: %v", cfg.GlobalLogging)
}
