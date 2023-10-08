package api

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

const defaultFeeAmount = 500

func initCompactOutputsCmd(apiCmd *cobra.Command) {
	getOutputsCmd := &cobra.Command{
		Use:   "compact",
		Short: `compacts all non-chain outputs unlockable now into one ED25519 output`,
		Args:  cobra.NoArgs,
		Run:   runCompactCmd,
	}

	getOutputsCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(getOutputsCmd)
}

func runCompactCmd(_ *cobra.Command, _ []string) {
	var tagAlongSeqID *core.ChainID
	feeAmount := getTagAlongFee() // 0 interpreted as no fee output
	if feeAmount > 0 {
		tagAlongSeqID = GetTagAlongSequencerID()
		md, err := getClient().GetMilestoneDataFromHeaviestState(*tagAlongSeqID)
		glb.AssertNoError(err)

		if feeAmount > 0 {
			if md != nil && md.MinimumFee > feeAmount {
				feeAmount = md.MinimumFee
			}
		}
	}

	walletData := glb.GetWalletData()
	nowisTs := core.LogicalTimeNow()
	walletOutputs, err := getClient().GetAccountOutputs(walletData.Account, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		if idx != 0xff {
			return false
		}
		return o.Lock().UnlockableWith(walletData.Account.AccountID(), nowisTs)
	})
	glb.AssertNoError(err)

	glb.Infof("%d ED25519 output(s) are unlockable now in the wallet account %s", len(walletOutputs), walletData.Account.String())
	if len(walletOutputs) <= 1 {
		glb.Infof("no need for compacting")
		os.Exit(0)
	}

	var prompt string
	if feeAmount > 0 {
		prompt = fmt.Sprintf("compacting will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.Short())
	} else {
		prompt = "compacting transaction will not have tag-along fee output (fee-less). Proceed?"
	}
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := getClient().MakeCompactTransaction(walletData.PrivateKey, tagAlongSeqID, feeAmount)
	if err != nil {
		if txCtx != nil {
			glb.Verbosef("------- failed transaction -------- \n%s\n--------------------------", txCtx.String())
		}
		glb.AssertNoError(err)
	}
	glb.Infof("Submitting compact transaction with %d inputs..", txCtx.NumInputs())
	err = getClient().SubmitTransaction(txCtx.TransactionBytes())
	glb.AssertNoError(err)

	if !NoWait() {
		glb.AssertNoError(waitForInclusion(txCtx.OutputID(0)))
	}
}
