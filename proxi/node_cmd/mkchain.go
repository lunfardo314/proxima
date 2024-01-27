package node_cmd

import (
	"os"
	"strconv"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initMakeChainCmd() *cobra.Command {
	getMakeChainCmd := &cobra.Command{
		Use:   "mkchain [<initial on-chain balance>]",
		Short: `creates new chain origin (not a sequencer)`,
		Args:  cobra.MaximumNArgs(1),
		Run:   runMakeChainCmd,
	}
	getMakeChainCmd.InitDefaultHelpCmd()
	return getMakeChainCmd
}

const (
	defaultOnChainAmount = uint64(1_000_000)
	defaultTagAlongFee   = 500
	minimumFee           = defaultTagAlongFee
)

func runMakeChainCmd(_ *cobra.Command, args []string) {
	wallet := glb.GetWalletData()
	target := glb.MustGetTarget()

	tagAlongSeqID := GetTagAlongSequencerID()
	tagAlongFee := viper.GetUint64("tag-along.fee")
	if tagAlongFee < minimumFee {
		tagAlongFee = defaultTagAlongFee
	}
	var err error
	onChainAmount := defaultOnChainAmount
	if len(args) == 1 {
		onChainAmount, err = strconv.ParseUint(args[0], 10, 64)
		glb.AssertNoError(err)
	}

	glb.Infof("Creating new chain origin:")
	glb.Infof("   on-chain balance: %s", util.GoTh(onChainAmount))
	glb.Infof("   tag-along fee %s to the sequencer %s", util.GoTh(tagAlongFee), tagAlongSeqID)
	glb.Infof("   source account: %s", wallet.Account)
	glb.Infof("   total cost: %s", util.GoTh(onChainAmount+tagAlongFee))
	glb.Infof("   chain controller: %s", target)

	if !glb.YesNoPrompt("proceed?:", false) {
		glb.Infof("exit")
		os.Exit(0)
	}

	ts := ledger.LogicalTimeNow()
	inps, totalInputs, err := getClient().GetTransferableOutputs(wallet.Account, ts)
	glb.AssertNoError(err)
	glb.Assertf(totalInputs >= onChainAmount+tagAlongFee, "not enough source balance %s", util.GoTh(totalInputs))

	totalInputs = 0
	inps = util.FilterSlice(inps, func(o *ledger.OutputWithID) bool {
		if totalInputs < onChainAmount+tagAlongFee {
			totalInputs += o.Output.Amount()
			return true
		}
		return false
	})

	txCtx, chainID, err := getClient().MakeChainOrigin(client.TransferFromED25519WalletParams{
		WalletPrivateKey: wallet.PrivateKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      tagAlongFee,
		Amount:           onChainAmount,
		Target:           target.AsLock(),
	})
	glb.AssertNoError(err)
	glb.Infof("new chain ID will be %s", chainID.String())
	if !NoWait() {
		glb.AssertNoError(waitForInclusion(txCtx.OutputID(0)))
	}
}
