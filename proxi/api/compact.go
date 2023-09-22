package api

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/txutils"
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

	console.Infof("%d ED25519 output(s) are unlockable know in the wallet account %s", len(walletOutputs), wallet.String())
	if len(walletOutputs) <= 1 {
		console.Infof("no need for compacting")
		os.Exit(0)
	}

	tagAlongSeqID := glb.GetSequencerID()
	console.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	prompt := fmt.Sprintf("compacting will cost %d of fees paid to the tag-along sequencer %s. Proceed?", defaultFeeAmount, tagAlongSeqID.Short())
	if !console.YesNoPrompt(prompt, true) {
		console.Infof("exit")
		os.Exit(0)
	}

	numCompacted, txStr, err := getClient().CompactED25519Outputs(glb.GetPrivateKey(), *tagAlongSeqID, defaultFeeAmount)
	if err != nil {
		if txStr != "" {
			console.Verbosef("------- transfer transaction -------- \n%s\n--------------------------", txStr)
		}
		console.AssertNoError(err)
	}
	console.Infof("Success: %d outputs have been compacted into one", numCompacted)
}
