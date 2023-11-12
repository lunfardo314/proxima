package api

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initSeqWithdrawCmd(seqCmd *cobra.Command) {
	seqSendCmd := &cobra.Command{
		Use:     "withdraw <amount>",
		Aliases: util.List("send"),
		Short:   `withdraw tokens from sequencer to the target lock`,
		Args:    cobra.ExactArgs(1),
		Run:     runSeqWithdrawCmd,
	}

	seqSendCmd.InitDefaultHelpCmd()
	seqCmd.AddCommand(seqSendCmd)
}

func runSeqWithdrawCmd(_ *cobra.Command, args []string) {
	seqID := glb.GetOwnSequencerID()
	glb.Assertf(seqID != nil, "can't get own sequencer ID")
	glb.Infof("sequencer ID (source): %s", seqID.String())

	md, err := getClient().GetMilestoneDataFromHeaviestState(*seqID)
	glb.AssertNoError(err)

	// TODO ensure at least storage deficit
	feeAmount := viper.GetUint64("tag-along.fee")
	if md != nil && md.MinimumFee > feeAmount {
		feeAmount = md.MinimumFee
	}

	walletData := glb.GetWalletData()
	glb.Infof("wallet account is: %s", walletData.Account.String())
	targetLock := glb.MustGetTarget()

	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	glb.Assertf(amount >= sequencer.MinimumAmountToRequestFromSequencer, "amount must be at least %s",
		util.GoThousands(sequencer.MinimumAmountToRequestFromSequencer))

	glb.Infof("amount: %s", util.GoThousands(amount))

	glb.Infof("querying wallet's outputs..")
	walletOutputs, err := getClient().GetAccountOutputs(walletData.Account, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		return idx == 0xff
	})
	glb.AssertNoError(err)

	glb.Infof("will be using fee amount of %d from the wallet. Outputs in the wallet:", feeAmount)
	for i, o := range walletOutputs {
		glb.Infof("%d : %s : %s", i, o.ID.Short(), util.GoThousands(o.Output.Amount()))
	}

	prompt := fmt.Sprintf("withdraw %s from %s to the target %s?",
		util.GoThousands(amount), seqID.Short(), targetLock.String())
	if !glb.YesNoPrompt(prompt, false) {
		glb.Infof("exit")
		return
	}

	var amountBin [8]byte
	binary.BigEndian.PutUint64(amountBin[:], amount)
	cmdParArr := lazybytes.MakeArrayFromDataReadOnly(targetLock.Bytes(), amountBin[:])
	cmdData := common.Concat(sequencer.CommandCodeWithdrawAmount, cmdParArr)
	constrSource := fmt.Sprintf("concat(0x%s)", hex.EncodeToString(cmdData))
	cmdConstr, err := core.NewGeneralScriptFromSource(constrSource)
	glb.AssertNoError(err)

	transferData := txbuilder.NewTransferData(walletData.PrivateKey, walletData.Account, core.LogicalTimeNow()).
		WithAmount(feeAmount).
		WithTargetLock(core.ChainLockFromChainID(*seqID)).
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
}
