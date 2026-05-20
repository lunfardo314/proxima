package seq_cmd

import (
	"fmt"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initSeqWithdrawCmd() *cobra.Command {
	seqSendCmd := &cobra.Command{
		Use:     "withdraw <amount>",
		Aliases: util.List("send"),
		Short:   `withdraw tokens from sequencer to controller's account or to the the target lock`,
		Args:    cobra.ExactArgs(1),
		Run:     runSeqWithdrawCmd,
	}

	glb.AddFlagTarget(seqSendCmd)

	seqSendCmd.InitDefaultHelpCmd()
	return seqSendCmd
}

func runSeqWithdrawCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()
	glb.Assertf(walletData.Sequencer != nil, "can't get own sequencer id")
	glb.Infof("sequencer id (source): %s", walletData.Sequencer.String())

	glb.Infof("wallet account is: %s", walletData.Account.String())
	targetLock := glb.MustGetTarget()

	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	glb.Infof("amount: %s", util.Th(amount))

	// Get the minimum tag-along fee from the sequencer
	fee, err := glb.GetRequiredTagAlongFee(*walletData.Sequencer)
	if err != nil {
		glb.Infof("error getting tag-along fee: %s", err)
		return
	}
	glb.Verbosef("tag-along fee: %s", util.Th(fee))

	tagAlongOut := txbuilder_seq.NewWithdrawRequestOutput(*walletData.Sequencer, walletData.Account, fee, amount, targetLock)
	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(12)
	}
	txBytes, txid, txString, err := glb.MakeSendOutputTransaction(tagAlongOut, walletData.PrivateKey, ts)
	if err != nil {
		glb.Infof("error: %s", err)
		if txString != "" {
			glb.Infof("------------ failing tx ---------------\n" + txString)
		}
		return
	}

	glb.Verbosef("---- request transaction ------\n%s\n------------------", txString)
	prompt := fmt.Sprintf("\nwithdraw %s from sequencer %s?", util.Th(amount), walletData.Sequencer.String())
	if !glb.YesNoPrompt(prompt, true) {
		return
	}
	glb.Infof("submitting the transaction...")

	err = glb.GetClient().SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}
