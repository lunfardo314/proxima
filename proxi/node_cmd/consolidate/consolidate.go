// Package consolidate is `proxi node consolidate`: the wallet consolidator of
// package consolidator run alone on the wallet profile. It reads the
// profile's 'consolidate' section and the flags into a consolidator.Config;
// the miner reads the same section to run the consolidator beside itself.
package consolidate

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/consolidator"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const (
	// SendToOwn is the send_to_sequencer value naming the wallet's own sequencer.
	SendToOwn = "own"
	// DelegateRandom is the autodelegate value drawing a target on every action. It is
	// the default: a wallet that only mines, or never chose a target, still puts its
	// payouts to work instead of leaving them diluted.
	DelegateRandom = "random"
	// DelegateNone is the autodelegate value that only compacts.
	DelegateNone = "none"
)

func Init() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consolidate",
		Short: "run permanently: periodically consolidate the wallet's scattered outputs and put what is above the minimum back into consensus",
		Long: `Runs until interrupted. Every 10 seconds it reads the wallet account and, when
there is enough to act on, builds one transaction that consumes up to
max_inputs of the smallest plain sigLock outputs and reclaimable tag-along
outputs, sequencer requests the target never took included. What is above the
configured minimum balance is sent to a sequencer
(send_to_sequencer) or delegated (autodelegate, 'random' unless set otherwise);
with 'autodelegate: none' it is folded into a single output back to the wallet.

Configured in the 'consolidate' section of the wallet profile; every flag
below overrides the profile key of the same name. See kb/consolidate.md.
'proxi node mine' runs the same consolidator beside the miner unless told not
to, so a mining wallet needs this command only with --disable_consolidation.`,
		Args: cobra.NoArgs,
		Run:  run,
	}
	cmd.Flags().Uint64("threshold-prox", consolidator.DefaultThresholdPROX, "act once the consolidatable balance exceeds this, in PROX (not motes)")
	cmd.Flags().Uint64("minimum-balance-prox", consolidator.DefaultMinimumBalancePROX, "balance always kept in the wallet on plain sigLock outputs, in PROX (not motes)")
	cmd.Flags().Int("max-inputs", consolidator.DefaultMaxInputs, "most outputs one consolidating transaction consumes (2-256)")
	cmd.Flags().Int("compact-at", consolidator.DefaultCompactAt, "compact as soon as this many consolidatable outputs have piled up, even below the threshold")
	cmd.Flags().String("send-to-sequencer", "", "'own' sends everything above the minimum to wallet.sequencer_id, a sequencer ID sends it to that sequencer, empty disables")
	cmd.Flags().String("autodelegate", "", "when sending is disabled: 'random' (the default) delegates to a sequencer drawn on every action, a sequencer ID delegates to that one, 'none' only compacts")
	cmd.Flags().Int("target-delegations", consolidator.DefaultTargetDelegations, "number of own delegations to build up to; beyond it existing ones are topped up or folded together")
	cmd.Flags().Uint64("target-delegation-prox", consolidator.DefaultTargetSizePROX, "size a delegation is grown to before the next one is created, in PROX (not motes)")
	cmd.Flags().Int("max-delegations", 0, "earlier name of --target-delegations, read when that one is not given")
	cmd.Flags().Duration("status-period", consolidator.DefaultStatusPeriod, "how often to report the account while no action is taken, 0 disables")
	_ = cmd.Flags().MarkHidden("max-delegations")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func run(cmd *cobra.Command, _ []string) {
	cfg, err := ReadConfig(cmd.Flags(), glb.GetLedgerConstants())
	glb.AssertNoError(err)
	k, err := consolidator.New(cfg, Environment(os.Stdout))
	glb.AssertNoError(err)
	k.Run(context.Background())
}

// Environment is the consolidator's view of this wallet process: its node
// client, library, constants and key, writing to w.
func Environment(w io.Writer) consolidator.Environment {
	return consolidator.Environment{
		Client:     glb.GetClient(),
		Library:    glb.GetTxLibrary(),
		Constants:  glb.GetLedgerConstants(),
		PrivateKey: glb.GetWalletData().PrivateKey,
		Log:        w,
		Verbose:    glb.IsVerbose(),
	}
}

// ReadConfig resolves each setting from its flag when given, else from the
// profile key; flags may be nil, as when the miner reads the section. An
// unparsable sequencer ID disables its mode with a warning rather than
// failing, as the spec asks, but never silently. The tag-along target is the
// profile's, verified on the ledger unless it is 'random'.
func ReadConfig(flags *pflag.FlagSet, consts *txbuildercore.Constants) (consolidator.Config, error) {
	cfg := consolidator.Config{
		Threshold:    uint64Setting(flags, "threshold-prox", "consolidate.threshold_prox", consolidator.DefaultThresholdPROX) * consts.SmallestAmountsPerBaseToken,
		Minimum:      uint64Setting(flags, "minimum-balance-prox", "consolidate.minimum_balance_prox", consolidator.DefaultMinimumBalancePROX) * consts.SmallestAmountsPerBaseToken,
		MaxInputs:    intSetting(flags, "max-inputs", "consolidate.max_inputs", consolidator.DefaultMaxInputs),
		CompactAt:    intSetting(flags, "compact-at", "consolidate.compact_at", consolidator.DefaultCompactAt),
		TargetSize:   uint64Setting(flags, "target-delegation-prox", "consolidate.target_delegation_prox", consolidator.DefaultTargetSizePROX) * consts.SmallestAmountsPerBaseToken,
		StatusPeriod: durationSetting(flags, "status-period", "consolidate.status_period", consolidator.DefaultStatusPeriod),
		TagAlongFee:  glb.GetTagAlongFee(),
	}
	// max_delegations is the earlier name of target_delegations; the new name wins
	cfg.TargetDelegations = intSetting(flags, "target-delegations", "consolidate.target_delegations", consolidator.DefaultTargetDelegations)
	if !changed(flags, "target-delegations") && !viper.IsSet("consolidate.target_delegations") {
		if legacy := intSetting(flags, "max-delegations", "consolidate.max_delegations", 0); legacy > 0 {
			cfg.TargetDelegations = legacy
		}
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}

	switch v := strings.TrimSpace(stringSetting(flags, "send-to-sequencer", "consolidate.send_to_sequencer")); v {
	case "":
	case SendToOwn:
		// wallet.sequencer_id is read directly: glb.GetOwnSequencerID falls back
		// to the default sequencer, and 'own' must never mean somebody else's
		ownStr := viper.GetString("wallet.sequencer_id")
		if ownStr == "" {
			glb.Infof("WARNING: send_to_sequencer is '%s' but wallet.sequencer_id is not set: sending to a sequencer is disabled", SendToOwn)
			break
		}
		own, err := base.ChainIDFromHexString(ownStr)
		if err != nil {
			glb.Infof("WARNING: wallet.sequencer_id '%s' is not a chain ID (%v): sending to a sequencer is disabled", ownStr, err)
			break
		}
		cfg.SendOwn, cfg.SendTo = true, &own
	default:
		id, err := base.ChainIDFromHexString(v)
		if err != nil {
			glb.Infof("WARNING: send_to_sequencer '%s' is neither '%s' nor a sequencer ID (%v): sending to a sequencer is disabled", v, SendToOwn, err)
			break
		}
		cfg.SendTo = &id
	}

	autodelegate := strings.TrimSpace(stringSetting(flags, "autodelegate", "consolidate.autodelegate"))
	switch autodelegate {
	case "", DelegateRandom:
		cfg.DelegateRandom = true
	case DelegateNone:
	default:
		id, err := base.ChainIDFromHexString(autodelegate)
		if err != nil {
			glb.Infof("WARNING: autodelegate '%s' is neither '%s', '%s' nor a sequencer ID (%v): delegation is disabled", autodelegate, DelegateRandom, DelegateNone, err)
			break
		}
		cfg.DelegateTo = &id
	}
	if cfg.SendTo != nil && autodelegate != "" && autodelegate != DelegateNone {
		glb.Infof("note: autodelegate is ignored while send_to_sequencer is set")
	}
	// a top-up of a frozen delegation goes through the target and must carry at
	// least the ledger's minimum top-up; below that, frozen delegations would
	// wait for an amount the trigger never accumulates
	if (cfg.DelegateRandom || cfg.DelegateTo != nil) && cfg.SendTo == nil && cfg.Threshold-cfg.Minimum < txbuildercore.MinimumTopUpAmount {
		glb.Infof("WARNING: threshold_prox less minimum_balance_prox is under the minimum top-up of %s: frozen delegations will never be topped up",
			util.Th(txbuildercore.MinimumTopUpAmount))
	}

	if viper.GetString("tag_along.sequencer_id") != glb.TagAlongSequencerRandom {
		seqID := glb.GetTagAlongSequencerID() // verified on the ledger
		if seqID == nil {
			return cfg, fmt.Errorf("tag-along sequencer not specified")
		}
		cfg.TagAlongSeqID = seqID
	}
	return cfg, nil
}

func changed(flags *pflag.FlagSet, flag string) bool {
	return flags != nil && flags.Changed(flag)
}

func stringSetting(flags *pflag.FlagSet, flag, key string) string {
	if changed(flags, flag) {
		v, _ := flags.GetString(flag)
		return v
	}
	return viper.GetString(key)
}

func intSetting(flags *pflag.FlagSet, flag, key string, def int) int {
	if changed(flags, flag) {
		v, _ := flags.GetInt(flag)
		return v
	}
	if viper.IsSet(key) {
		return viper.GetInt(key)
	}
	return def
}

func durationSetting(flags *pflag.FlagSet, flag, key string, def time.Duration) time.Duration {
	if changed(flags, flag) {
		v, _ := flags.GetDuration(flag)
		return v
	}
	if viper.IsSet(key) {
		return viper.GetDuration(key)
	}
	return def
}

func uint64Setting(flags *pflag.FlagSet, flag, key string, def uint64) uint64 {
	if changed(flags, flag) {
		v, _ := flags.GetUint64(flag)
		return v
	}
	if viper.IsSet(key) {
		return viper.GetUint64(key)
	}
	return def
}
