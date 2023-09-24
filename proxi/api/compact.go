package api

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
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

	wallet := glb.GetWalletAccount()
	nowisTs := core.LogicalTimeNow()
	walletOutputs, err := getClient().GetAccountOutputs(wallet, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		if idx != 0xff {
			return false
		}
		return o.Lock().UnlockableWith(wallet.AccountID(), nowisTs)
	})
	console.AssertNoError(err)

	console.Infof("%d ED25519 output(s) are unlockable now in the wallet account %s", len(walletOutputs), wallet.String())
	if len(walletOutputs) <= 1 {
		console.Infof("no need for compacting")
		os.Exit(0)
	}

	var prompt string
	if feeAmount > 0 {
		prompt = fmt.Sprintf("compacting will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.Short())
	} else {
		prompt = "compacting transaction will not have tag-along fee output (fee-less). Proceed?"
	}
	if !console.YesNoPrompt(prompt, true) {
		console.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := getClient().CompactED25519Outputs(glb.GetPrivateKey(), tagAlongSeqID, feeAmount)
	if err != nil {
		if txCtx != nil {
			console.Verbosef("------- failed transaction -------- \n%s\n--------------------------", txCtx.String())
		}
		console.AssertNoError(err)
	}
	console.Infof("Success: %d outputs have been compacted into one", txCtx.NumInputs())
}
