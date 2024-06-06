package seq_cmd

import (
	"fmt"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/factory/commands"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initSeqWithdrawCmd() *cobra.Command {
	seqSendCmd := &cobra.Command{
		Use:     "withdraw <amount>",
		Aliases: util.List("send"),
		Short:   `withdraw tokens from sequencer to the target lock`,
		Args:    cobra.ExactArgs(1),
		Run:     runSeqWithdrawCmd,
	}
	seqSendCmd.InitDefaultHelpCmd()
	return seqSendCmd
}

const ownSequencerCmdFee = 500

func runSeqWithdrawCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()
	glb.Assertf(walletData.Sequencer != nil, "can't get own sequencer ID")
	glb.Infof("sequencer ID (source): %s", walletData.Sequencer.String())

	glb.Infof("wallet account is: %s", walletData.Account.String())
	targetLock := glb.MustGetTarget()

	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	glb.Infof("amount: %s", util.Th(amount))

	glb.Infof("querying wallet's outputs..")
	walletOutputs, err := getClient().GetAccountOutputs(walletData.Account, func(_ *ledger.OutputID, o *ledger.Output) bool {
		return o.NumConstraints() == 2
	})
	glb.AssertNoError(err)

	glb.Infof("will be using %d tokens as tag-along fee. Outputs in the wallet:", ownSequencerCmdFee)
	for i, o := range walletOutputs {
		glb.Infof("%d : %s : %s", i, o.ID.StringShort(), util.Th(o.Output.Amount()))
	}

	prompt := fmt.Sprintf("withdraw %s from %s to the target %s?",
		util.Th(amount), walletData.Sequencer.StringShort(), targetLock.String())
	if !glb.YesNoPrompt(prompt, true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		return
	}

	cmdConstr, err := commands.MakeSequencerWithdrawCommand(amount, targetLock.AsLock())
	glb.AssertNoError(err)

	transferData := txbuilder.NewTransferData(walletData.PrivateKey, walletData.Account, ledger.TimeNow()).
		WithAmount(ownSequencerCmdFee).
		WithTargetLock(ledger.ChainLockFromChainID(*walletData.Sequencer)).
		MustWithInputs(walletOutputs...).
		WithSender().
		WithConstraint(cmdConstr)

	txBytes, err := txbuilder.MakeSimpleTransferTransaction(transferData)
	glb.AssertNoError(err)

	txStr := transaction.ParseBytesToString(txBytes, transaction.PickOutputFromListFunc(walletOutputs))

	glb.Verbosef("---- request transaction ------\n%s\n------------------", txStr)

	glb.Infof("submitting the transaction...")

	err = getClient().SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	if glb.NoWait() {
		return
	}
	txid, err := transaction.IDFromTransactionBytes(txBytes)
	glb.AssertNoError(err)

	glb.ReportTxInclusion(txid, time.Second)
}
