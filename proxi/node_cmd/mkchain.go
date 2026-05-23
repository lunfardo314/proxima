package node_cmd

import (
	"crypto/ed25519"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

// Chain-origin flags shared by mkchain and setup_seq.
// See claude/delegation_epoch_params.md.
var (
	flagAcceptDelegations         bool
	flagDelegationEpochSlots      uint32
	flagDelegationMaxFrozenEpochs uint8
)

func initMakeChainCmd() *cobra.Command {
	makeChainCmd := &cobra.Command{
		Use:   "mkchain <initial on-chain balance>",
		Short: `creates new chain origin (not a sequencer)`,
		Args:  cobra.ExactArgs(1),
		Run:   runMakeChainCmd,
	}
	addDelegationParamsFlags(makeChainCmd, false /* default: opt-out for regular chains */)
	makeChainCmd.InitDefaultHelpCmd()

	return makeChainCmd
}

// addDelegationParamsFlags wires --accept-delegations,
// --delegation-epoch-slots and --delegation-max-frozen-epochs onto the
// given command. defaultOptIn controls --accept-delegations's default:
// sequencer setup defaults to true (a sequencer that can't accept
// delegations is useless and refused by the sequencer's startup
// precondition); regular mkchain defaults to false.
//
// The two numeric flag defaults are 0 sentinels meaning "use the
// server's library values". Real values are resolved at Run time via
// glb.GetLedgerConstants() — see chainOriginDelegationParams.
// Registering with 0 sentinels avoids reaching for the ledger
// singleton at cobra-init time (which used to panic when the
// singleton wasn't yet populated; `proxi node mkchain --help` on a
// fresh clone hit that path).
func addDelegationParamsFlags(cmd *cobra.Command, defaultOptIn bool) {
	cmd.PersistentFlags().BoolVar(&flagAcceptDelegations, "accept-delegations", defaultOptIn,
		"attach delegationParams at chain origin so the chain can accept delegations (sequencer chains REQUIRE this)")
	cmd.PersistentFlags().Uint32Var(&flagDelegationEpochSlots, "delegation-epoch-slots", 0,
		"target delegation epoch length in slots (0 = use server default; only consulted when --accept-delegations is set)")
	cmd.PersistentFlags().Uint8Var(&flagDelegationMaxFrozenEpochs, "delegation-max-frozen-epochs", 0,
		"target maximum simultaneous frozen epochs (0 = use server default; only consulted when --accept-delegations is set)")
}

// delegationParamsValues is the small (epochSlots, maxFrozenEpochs)
// pair the chain-origin output's delegationParams constraint carries.
// Kept as a local struct (instead of *ledger.DelegationParams) so the
// flag-resolution path doesn't touch the ledger singleton.
type delegationParamsValues struct {
	EpochSlots      uint32
	MaxFrozenEpochs byte
}

// resolveChainOriginDelegationParams reads the flags + the live
// server constants and returns the delegationParams to attach at
// chain origin — or nil to opt out. Flag 0 sentinels are filled in
// from glb.GetLedgerConstants().
func resolveChainOriginDelegationParams() *delegationParamsValues {
	if !flagAcceptDelegations {
		return nil
	}
	consts := glb.GetLedgerConstants()
	epochSlots := flagDelegationEpochSlots
	if epochSlots == 0 {
		epochSlots = consts.DelegationEpochSlots
	}
	maxFrozen := flagDelegationMaxFrozenEpochs
	if maxFrozen == 0 {
		maxFrozen = byte(consts.MaxFrozenEpochs)
	}
	return &delegationParamsValues{EpochSlots: epochSlots, MaxFrozenEpochs: maxFrozen}
}

// MakeChain is the entry point used both by `proxi node mkchain` and
// by `proxi node setup_seq`. It builds + submits a chain-origin tx
// for `onChainAmount` to the wallet's currently configured target
// lock, returning the new chain ID + the tx ID for downstream
// inclusion tracking. Pure wasm-style: no ledger.L() singleton, no
// ledger/txbuilder sugar.
func MakeChain(onChainAmount uint64) (txBytes []byte, chainID base.ChainID, txid base.TransactionID, err error) {
	walletData := glb.GetWalletData()
	target := glb.MustGetTarget()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	seqMinFee, err := glb.GetSequencerMinimumFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	feeAmount := glb.GetTagAlongFee()
	if seqMinFee > feeAmount {
		feeAmount = seqMinFee
	}

	glb.Infof("Creating new chain origin:")
	glb.Infof("   on-chain balance: %s", util.Th(onChainAmount))
	glb.Infof("   tag-along fee %s to the sequencer %s", util.Th(feeAmount), tagAlongSeqID.String())
	glb.Infof("   source account: %s", walletData.Account.String())
	glb.Infof("   total cost: %s", util.Th(onChainAmount+feeAmount))
	glb.Infof("   chain controller: %s", target)

	if !glb.YesNoPrompt("proceed?:", true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Fetch the wallet's sigLock outputs and trim to the minimum set
	// that covers (onChainAmount + feeAmount). Server returns them
	// sorted; we take the head.
	inps, lrbid, totalInputs, err := glb.GetClient().GetTransferableOutputs(walletData.Account)
	glb.AssertNoError(err)
	if onChainAmount == 0 {
		// special case: transfer the maximum possible amount on chain
		onChainAmount = totalInputs - feeAmount
	}
	glb.Assertf(totalInputs >= onChainAmount+feeAmount, "not enough source balance %s", util.Th(totalInputs))
	glb.PrintLRB(lrbid)

	picked := uint64(0)
	inps = util.PurgeSlice(inps, func(o *ledger.OutputWithID) bool {
		if picked < onChainAmount+feeAmount {
			picked += o.Output.TokenBalance()
			return true
		}
		return false
	})

	dp := resolveChainOriginDelegationParams()
	if dp != nil {
		glb.Infof("   delegationParams: epochSlots=%d, maxFrozenEpochs=%d (chain can accept delegations)",
			dp.EpochSlots, dp.MaxFrozenEpochs)
	} else {
		glb.Infof("   delegationParams: omitted (chain cannot accept delegations)")
	}

	// Wallet-derived "now" — wall-clock mapped through genesis +
	// tick-duration; pace-enforced against each input timestamp.
	consts := glb.GetLedgerConstants()
	ts := consts.LedgerTimeFromClockTime(time.Now())
	for _, in := range inps {
		ts = base.MaximumTime(ts, in.Timestamp().AddTicks(int(consts.TransactionPace)))
	}
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}

	var consumed [][]byte
	var chainOutIdx byte
	txBytes, txid, chainOutIdx, consumed, err = makeChainOriginTransaction(
		walletData.PrivateKey, inps, target, onChainAmount, dp,
		*tagAlongSeqID, feeAmount, ts)
	glb.AssertNoError(err)

	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		return nil, base.NilChainID, base.TransactionID{}, err
	}

	chainOid := base.MustNewOutputID(txid, chainOutIdx)
	chainID = base.MakeOriginChainID(chainOid)
	return txBytes, chainID, txid, nil
}

// makeChainOriginTransaction is the pure wasm-wallet compose helper:
// consumes the supplied wallet sigLock inputs and produces the chain
// origin (target lock + chainOrigin at slot 3 + optional delegationParams
// at slot 6), a tag-along output, and a PRXI remainder back to the
// wallet. No I/O; no ledger.L() singleton; no ledger/txbuilder sugar.
//
// Input unlock pattern matches the compact/send template:
// PutSignatureUnlock(0) on input 0 + PutUnlockReference(i,
// ConstraintIndexLock, 0) on the rest — the reference path skips
// the txHolderID hash compare for homogeneous sigLock inputs.
func makeChainOriginTransaction(
	walletPrivateKey ed25519.PrivateKey,
	walletOutputs []*ledger.OutputWithID,
	target ledger.Lock,
	onChainAmount uint64,
	dp *delegationParamsValues,
	tagAlongSeqID base.ChainID,
	tagAlongFee uint64,
	ts base.LedgerTime,
) (txBytes []byte, txid base.TransactionID, chainOutIdx byte, consumed [][]byte, err error) {
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletPrivateKey)
	txb := txbuildercore.New(0)

	consumedTotal := uint64(0)
	consumed = make([][]byte, 0, len(walletOutputs))
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		consumedTotal += in.Output.TokenBalance()
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			if err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
				return nil, base.TransactionID{}, 0, nil, err
			}
		}
	}

	// Chain-origin output: target lock + chainOrigin at slot 3 +
	// optional delegationParams at slot 6. Built by extending a base
	// sigLock or chainLock output.
	baseChainOut, err := glb.BuildLockOutput(lib, onChainAmount, target)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBuilder, err := txbuildercore.OutputBuilderFromBytes(baseChainOut.Bytes())
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBin, err := lib.NewChainOrigin(ts.Slot)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBuilder.MustPushConstraint(chainOriginBin)
	if dp != nil {
		dpBin, err := lib.NewDelegationParams(dp.EpochSlots, dp.MaxFrozenEpochs)
		if err != nil {
			return nil, base.TransactionID{}, 0, nil, err
		}
		chainOriginBuilder.PutConstraint(dpBin, ledger.ConstraintIndexDelegationParams)
	}
	chainOutIdx = txb.ProduceOutput(chainOriginBuilder.Output().Bytes())

	if tagAlongFee > 0 {
		tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, tagAlongFee, tagAlongSeqID, walletHolderID)
		if err != nil {
			return nil, base.TransactionID{}, 0, nil, err
		}
		txb.ProduceOutput(tagAlongOut.Bytes())
	}
	if consumedTotal > onChainAmount+tagAlongFee {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, consumedTotal-onChainAmount-tagAlongFee, walletHolderID)
		if err != nil {
			return nil, base.TransactionID{}, 0, nil, err
		}
		txb.ProduceOutput(remainderOut.Bytes())
	}

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletPrivateKey)

	txBytes = txb.Bytes()
	txid, err = txbuildercore.TxIDFromBytes(txBytes)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	return txBytes, txid, chainOutIdx, consumed, nil
}

func runMakeChainCmd(_ *cobra.Command, args []string) {
	onChainAmount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	_, chainID, txid, err := MakeChain(onChainAmount)
	glb.AssertNoError(err)

	glb.Infof("new chain id will be %s", chainID.String())
	if !glb.NoWait() {
		glb.TrackTxInclusion(txid, time.Second)
	}
}
