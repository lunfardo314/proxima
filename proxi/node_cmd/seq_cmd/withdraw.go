package seq_cmd

import (
	"fmt"
	"strconv"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/factory"
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

	//md, err := getClient().GetMilestoneDataFromHeaviestState(*walletData.Sequencer)
	//glb.AssertNoError(err)

	glb.Infof("wallet account is: %s", walletData.Account.String())
	targetLock := glb.MustGetTarget()

	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	glb.Assertf(amount >= ledger.L().ID.MinimumAmountOnSequencer, "amount must be at least %s",
		util.GoTh(ledger.L().ID.MinimumAmountOnSequencer))

	glb.Infof("amount: %s", util.GoTh(amount))

	glb.Infof("querying wallet's outputs..")
	walletOutputs, err := getClient().GetAccountOutputs(walletData.Account, func(o *ledger.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		return idx == 0xff
	})
	glb.AssertNoError(err)

	glb.Infof("will be using fee amount of %d from the wallet. Outputs in the wallet:", ownSequencerCmdFee)
	for i, o := range walletOutputs {
		glb.Infof("%d : %s : %s", i, o.ID.StringShort(), util.GoTh(o.Output.Amount()))
	}

	prompt := fmt.Sprintf("withdraw %s from %s to the target %s?",
		util.GoTh(amount), walletData.Sequencer.StringShort(), targetLock.String())
	if !glb.YesNoPrompt(prompt, false) {
		glb.Infof("exit")
		return
	}

	cmdConstr, err := factory.MakeSequencerWithdrawCommand(amount, targetLock.AsLock())
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
	// TODO track inclusion
	glb.AssertNoError(err)
}
