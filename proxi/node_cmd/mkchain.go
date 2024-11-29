package node_cmd

import (
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initMakeChainCmd() *cobra.Command {
	makeChainCmd := &cobra.Command{
		Use:   "mkchain <initial on-chain balance>",
		Short: `creates new chain origin (not a sequencer)`,
		Args:  cobra.ExactArgs(1),
		Run:   runMakeChainCmd,
	}
	makeChainCmd.InitDefaultHelpCmd()

	return makeChainCmd
}

func MakeChain(onChainAmount uint64) (*transaction.TxContext, ledger.ChainID, error) {
	//cmd.DebugFlags()

	walletData := glb.GetWalletData()

	target := glb.MustGetTarget()

	var tagAlongSeqID *ledger.ChainID
	feeAmount := getTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")
	if feeAmount > 0 {
		tagAlongSeqID = GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		md, err := glb.GetClient().GetMilestoneData(*tagAlongSeqID)
		glb.AssertNoError(err)

		if md != nil && md.MinimumFee > feeAmount {
			feeAmount = md.MinimumFee
		}
	}
	glb.Infof("Creating new chain origin:")
	glb.Infof("   on-chain balance: %s", util.Th(onChainAmount))
	glb.Infof("   tag-along fee %s to the sequencer %s", util.Th(feeAmount), tagAlongSeqID)
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
			totalInputs += o.Output.Amount()
			return true
		}
		return false
	})

	return glb.GetClient().MakeChainOrigin(client.TransferFromED25519WalletParams{
		WalletPrivateKey: walletData.PrivateKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      feeAmount,
		Amount:           onChainAmount,
		Target:           target.AsLock(),
	})
}

func runMakeChainCmd(_ *cobra.Command, args []string) {
	//cmd.DebugFlags()
	glb.InitLedgerFromNode()

	onChainAmount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	txCtx, chainID, err := MakeChain(onChainAmount)

	glb.AssertNoError(err)
	glb.Infof("new chain ID is %s", chainID.String())
	if !glb.NoWait() {
		glb.ReportTxInclusion(*txCtx.TransactionID(), time.Second)
	}
}
