package api

import (
	"fmt"
	"os"
	"strconv"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/proxi/glb"
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

	var tagAlongSeqID *core.ChainID
	feeAmount := getTagAlongFee()
	if feeAmount > 0 {
		tagAlongSeqID = glb.GetSequencerID()
		console.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		md, err := getClient().GetMilestoneData(*tagAlongSeqID)
		console.AssertNoError(err)

		if feeAmount > 0 {
			if md != nil && md.MinimumFee > feeAmount {
				feeAmount = md.MinimumFee
			}
		}
	}

	var prompt string
	if feeAmount > 0 {
		prompt = fmt.Sprintf("trasfer will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.Short())
	} else {
		prompt = "transfer transaction will not have tag-along fee output (fee-less). Proceed?"
	}
	if !console.YesNoPrompt(prompt, true) {
		console.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := getClient().TransferFromED25519Wallet(client.TransferFromED25519WalletParams{
		WalletPrivateKey: glb.GetPrivateKey(),
		TagAlongSeqID:    *tagAlongSeqID,
		TagAlongFee:      feeAmount,
		Amount:           amount,
		Target:           target,
	})
	if err != nil {
		if txCtx != nil {
			console.Verbosef("-------- failed tx ---------\n%s\n----------------", txCtx.String())
		}
		console.AssertNoError(err)
	}
	console.Infof("success")
}
