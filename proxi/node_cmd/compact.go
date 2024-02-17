package node_cmd

import (
	"fmt"
	"os"
	"strconv"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

const (
	defaultMaxNumberOfInputs = 100
)

func initCompactOutputsCmd() *cobra.Command {
	getOutputsCmd := &cobra.Command{
		Use:   "compact [<max number of args. Default 100, maximum allowed 256>]",
		Short: `compacts all non-chain outputs unlockable now into one ED25519 output`,
		Args:  cobra.MaximumNArgs(1),
		Run:   runCompactCmd,
	}

	getOutputsCmd.InitDefaultHelpCmd()
	return getOutputsCmd
}

func runCompactCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	maxNumberOfInputs := defaultMaxNumberOfInputs
	var err error
	if len(args) > 0 {
		maxNumberOfInputs, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
		glb.Assertf(0 < maxNumberOfInputs && maxNumberOfInputs <= 256, "parameter must be > 0 and <= 256")
	}
	var tagAlongSeqID *ledger.ChainID
	feeAmount := getTagAlongFee() // 0 interpreted as no fee output
	if feeAmount > 0 {
		tagAlongSeqID = GetTagAlongSequencerID()
		md, err := glb.GetClient().GetMilestoneDataFromHeaviestState(*tagAlongSeqID)
		glb.AssertNoError(err)

		if feeAmount > 0 {
			if md != nil && md.MinimumFee > feeAmount {
				feeAmount = md.MinimumFee
			}
		}
	}

	walletData := glb.GetWalletData()
	nowisTs := ledger.TimeNow()
	walletOutputs, err := glb.GetClient().GetAccountOutputs(walletData.Account, func(o *ledger.Output) bool {
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
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")
	prompt = fmt.Sprintf("compacting will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := glb.GetClient().MakeCompactTransaction(walletData.PrivateKey, tagAlongSeqID, feeAmount, maxNumberOfInputs)
	if err != nil {
		if txCtx != nil {
			glb.Verbosef("------- failed transaction -------- \n%s\n--------------------------", txCtx.String())
		}
		glb.AssertNoError(err)
	}
	glb.Infof("Submitting compact transaction with %d inputs..", txCtx.NumInputs())
	err = glb.GetClient().SubmitTransaction(txCtx.TransactionBytes())
	glb.AssertNoError(err)

	if !NoWait() {
		glb.AssertNoError(waitForInclusion(txCtx.OutputID(0)))
	}
}
