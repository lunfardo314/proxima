package api

import (
	"fmt"
	"os"
	"strconv"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/spf13/cobra"
)

func initTransferCmd(apiCmd *cobra.Command) {
	getOutputsCmd := &cobra.Command{
		Use:   "transfer",
		Short: `sends tokens from the wallet's account to the target`,
		Args:  cobra.ExactArgs(2),
		Run:   runTransferCmd,
	}

	getOutputsCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getOutputsCmd)
}

func runTransferCmd(_ *cobra.Command, args []string) {
	amount, err := strconv.ParseUint(args[0], 10, 64)
	console.AssertNoError(err)

	target, err := core.LockFromSource(args[1])
	console.AssertNoError(err)

	tagAlongSeqID := glb.GetSequencerID()
	console.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	md, err := getClient().GetMilestoneData(*tagAlongSeqID)
	console.AssertNoError(err)

	feeAmount := uint64(defaultFeeAmount)
	if md != nil && md.MinimumFee > feeAmount {
		feeAmount = md.MinimumFee
	}

	wallet := glb.GetWalletAccount()

	oData, err := getClient().GetAccountOutputs(wallet)
	console.AssertNoError(err)

	nowisTs := core.LogicalTimeNow()
	walletOutputs, err := txutils.ParseAndSortOutputData(oData, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		if idx != 0xff {
			return false
		}
		return o.Lock().UnlockableWith(wallet.AccountID(), nowisTs)
	}, true)
	console.AssertNoError(err)

	console.Verbosef("%d ED25519 output(s) are unlockable now in the wallet account %s", len(walletOutputs), wallet.String())

	prompt := fmt.Sprintf("transfer will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.Short())
	if !console.YesNoPrompt(prompt, true) {
		console.Infof("exit")
		os.Exit(0)
	}

	txStr, err := getClient().TransferFromED25519Wallet(client.TransferFromED25519WalletParams{
		WalletPrivateKey: glb.GetPrivateKey(),
		TagAlongSeqID:    *tagAlongSeqID,
		TagAlongFee:      feeAmount,
		Amount:           amount,
		Target:           target,
	})
	if err != nil {
		if txStr != "" {
			console.Verbosef("-------- failed tx ---------\n%s\n----------------", txStr)
		}
		console.AssertNoError(err)
	}
	console.Infof("success")
}
