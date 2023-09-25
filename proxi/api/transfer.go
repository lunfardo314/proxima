package api

import (
	"fmt"
	"os"
	"strconv"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initTransferCmd(apiCmd *cobra.Command) {
	transferCmd := &cobra.Command{
		Use:   "transfer",
		Short: `sends tokens from the wallet's account to the target`,
		Args:  cobra.ExactArgs(1),
		Run:   runTransferCmd,
	}

	transferCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(transferCmd)
}

func runTransferCmd(_ *cobra.Command, args []string) {
	walletData := glb.GetWalletData()

	glb.Infof("source is the wallet account: %s", walletData.Account.String())
	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	target := glb.MustGetTarget()

	var tagAlongSeqID *core.ChainID
	feeAmount := getTagAlongFee()
	if feeAmount > 0 {
		tagAlongSeqID = glb.GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		md, err := getClient().GetMilestoneData(*tagAlongSeqID)
		glb.AssertNoError(err)

		if md != nil && md.MinimumFee > feeAmount {
			feeAmount = md.MinimumFee
		}
	}

	var prompt string
	if feeAmount > 0 {
		prompt = fmt.Sprintf("transfer will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.Short())
	} else {
		prompt = "transfer transaction will not have tag-along fee output (fee-less). Proceed?"
	}
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := getClient().TransferFromED25519Wallet(client.TransferFromED25519WalletParams{
		WalletPrivateKey: walletData.PrivateKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      feeAmount,
		Amount:           amount,
		Target:           target.AsLock(),
	})
	if err != nil {
		if txCtx != nil {
			glb.Verbosef("-------- failed tx ---------\n%s\n----------------", txCtx.String())
		}
		glb.AssertNoError(err)
	} else {
		glb.Verbosef("-------- transfer tx ---------\n%s\n----------------", txCtx.String())
	}
	glb.Infof("success")
}
