package node_cmd

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

const (
	defaultMaxNumberOfInputs = 100
)

func initCompactOutputsCmd() *cobra.Command {
	compactCmd := &cobra.Command{
		Use:   "compact [<max number of args. Default 100, maximum allowed 256>]",
		Short: `compacts up to <max number> non-chain outputs in the wallet account into one ED25519 output`,
		Args:  cobra.MaximumNArgs(1),
		Run:   runCompactCmd,
	}
	compactCmd.InitDefaultHelpCmd()
	return compactCmd
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
	feeAmount := getTagAlongFee()
	if feeAmount > 0 {
		tagAlongSeqID = GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		md, err := glb.GetClient().GetMilestoneData(*tagAlongSeqID)
		glb.AssertNoError(err)

		if md != nil && md.MinimumFee > feeAmount {
			feeAmount = md.MinimumFee
		}
	}
	walletData := glb.GetWalletData()
	walletOutputs, lrbid, err := glb.GetClient().GetAccountOutputsExt(walletData.Account, maxNumberOfInputs, "asc", func(_ *ledger.OutputID, o *ledger.Output) bool {
		return o.NumConstraints() == 2
	})
	glb.AssertNoError(err)

	glb.PrintLRB(lrbid)
	if len(walletOutputs) <= 1 {
		glb.Infof("only one output -> no need for compacting")
		os.Exit(0)
	}
	glb.Infof("%d ED25519 output(s) from account %s will be compacted into one", len(walletOutputs), walletData.Account.String())

	var prompt string
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")

	prompt = fmt.Sprintf("compacting will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := glb.GetClient().MakeCompactTransaction(walletData.PrivateKey, tagAlongSeqID, feeAmount, maxNumberOfInputs)
	if txCtx != nil {
		glb.Verbosef("------- the compacting transaction -------- \n%s\n--------------------------", txCtx.String())
	}
	glb.AssertNoError(err)
	glb.Assertf(txCtx != nil, "something wrong: transaction context is nil")
	txBytes := txCtx.TransactionBytes()
	glb.Infof("Submitting compacting transaction with %d inputs (%d bytes)..", txCtx.NumInputs(), len(txBytes))
	err = glb.GetClient().SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	if !glb.NoWait() {
		glb.ReportTxInclusion(*txCtx.TransactionID(), time.Second)
	}
}
