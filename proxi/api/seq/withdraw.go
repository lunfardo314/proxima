package api

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

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
		Use:     "withdraw",
		Aliases: util.List("send"),
		Short:   `withdraw tokens from sequencer to the target lock`,
		Args:    cobra.NoArgs,
		Run:     runSeqWithdrawCmd,
	}

	seqSendCmd.InitDefaultHelpCmd()
	seqCmd.AddCommand(seqSendCmd)
}

func runSeqWithdrawCmd(_ *cobra.Command, _ []string) {
	seqID := glb.GetOwnSequencerID()
	glb.Infof("sequencer ID (source): %s", seqID.String())

	md, err := getClient().GetMilestoneData(*seqID)
	glb.AssertNoError(err)

	// TODO ensure at least storage deficit
	feeAmount := viper.GetUint64("tag-along.fee")
	if md != nil && md.MinimumFee > feeAmount {
		feeAmount = md.MinimumFee
	}

	walletData := glb.GetWalletData()
	glb.Infof("wallet account is: %s", walletData.Account.String())
	targetLock := glb.MustGetTarget()
	glb.Infof("amount: %s", util.GoThousands(getAmount()))

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

	if !viper.GetBool("force") {
		prompt := fmt.Sprintf("withdraw %s from %s to the target %s?",
			util.GoThousands(getAmount()), seqID.Short(), targetLock.String())
		if !glb.YesNoPrompt(prompt, false) {
			glb.Infof("exit")
			return
		}
	}

	var amountBin [8]byte
	binary.BigEndian.PutUint64(amountBin[:], getAmount())
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

func getAmount() uint64 {
	return viper.GetUint64("amount")
}
