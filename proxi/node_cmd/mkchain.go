package node_cmd

import (
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
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
func addDelegationParamsFlags(cmd *cobra.Command, defaultOptIn bool) {
	lib := ledger.L(0)
	cmd.PersistentFlags().BoolVar(&flagAcceptDelegations, "accept-delegations", defaultOptIn,
		"attach delegationParams at chain origin so the chain can accept delegations (sequencer chains REQUIRE this)")
	cmd.PersistentFlags().Uint32Var(&flagDelegationEpochSlots, "delegation-epoch-slots", lib.DelegationEpochSlots,
		"target delegation epoch length in slots (only consulted when --accept-delegations is set; bounds enforced by EasyFL)")
	cmd.PersistentFlags().Uint8Var(&flagDelegationMaxFrozenEpochs, "delegation-max-frozen-epochs", uint8(lib.MaxFrozenEpochs),
		"target maximum simultaneous frozen epochs (only consulted when --accept-delegations is set; bounds enforced by EasyFL)")
}

// chainOriginDelegationParams returns the *ledger.DelegationParams to
// attach at chain origin based on the parsed flags, or nil to opt out.
func chainOriginDelegationParams() *ledger.DelegationParams {
	if !flagAcceptDelegations {
		return nil
	}
	return ledger.NewDelegationParams(flagDelegationEpochSlots, flagDelegationMaxFrozenEpochs)
}

func MakeChain(onChainAmount uint64) (*transaction.Transaction, base.ChainID, error) {
	//cmd.DebugFlags()

	walletData := glb.GetWalletData()

	target := glb.MustGetTarget()

	var tagAlongSeqID *base.ChainID
	feeAmount := glb.GetTagAlongFee()
	tagAlongSeqID = glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	md, err := glb.GetClient().GetSequencerData(*tagAlongSeqID)
	glb.AssertNoError(err)

	if md.MinimumFee() > feeAmount {
		feeAmount = md.MinimumFee()
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

	inps, lrbid, totalInputs, err := glb.GetClient().GetTransferableOutputs(walletData.Account)
	glb.AssertNoError(err)
	if onChainAmount == 0 {
		// transfer maximum possible amount on chain
		onChainAmount = totalInputs - feeAmount
	}
	glb.Assertf(totalInputs >= onChainAmount+feeAmount, "not enough source balance %s", util.Th(totalInputs))

	glb.PrintLRB(lrbid)
	totalInputs = 0
	inps = util.PurgeSlice(inps, func(o *ledger.OutputWithID) bool {
		if totalInputs < onChainAmount+feeAmount {
			totalInputs += o.Output.TokenBalance()
			return true
		}
		return false
	})

	dp := chainOriginDelegationParams()
	if dp != nil {
		glb.Infof("   delegationParams: epochSlots=%d, maxFrozenEpochs=%d (chain can accept delegations)",
			dp.EpochSlots, dp.MaxFrozenEpochs)
	} else {
		glb.Infof("   delegationParams: omitted (chain cannot accept delegations)")
	}
	return glb.MakeChainOrigin(glb.TransferFromED25519WalletParams{
		WalletPrivateKey: walletData.PrivateKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      feeAmount,
		Amount:           onChainAmount,
		Target:           target,
		DelegationParams: dp,
	})
}

func runMakeChainCmd(_ *cobra.Command, args []string) {
	//cmd.DebugFlags()
	glb.InitLedgerFromNode()

	onChainAmount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	tx, chainID, err := MakeChain(onChainAmount)
	glb.AssertNoError(err)
	err = tx.ValidateFullContext()
	glb.AssertNoError(err)

	glb.Infof("new chain id will be %s", chainID.String())
	if !glb.NoWait() {
		glb.TrackTxInclusion(tx.ID(), time.Second)
	}
}
