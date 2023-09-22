package api

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const defaultAmount = 1_000_000

func initSeqWithdrawCmd(seqCmd *cobra.Command) {
	seqSendCmd := &cobra.Command{
		Use:     "withdraw",
		Aliases: util.List("send"),
		Short:   `withdraw tokens from sequencer to the target lock`,
		Args:    cobra.NoArgs,
		Run:     runSeqWithdrawCmd,
	}

	seqSendCmd.Flags().Uint64P("amount", "a", defaultAmount, "amount to withdraw/send from sequencer")
	err := viper.BindPFlag("amount", seqSendCmd.Flags().Lookup("amount"))
	console.AssertNoError(err)

	seqSendCmd.Flags().BoolP("force_withdraw", "f", false, "force withdraw")
	err = viper.BindPFlag("force_withdraw", seqSendCmd.Flags().Lookup("force_withdraw"))
	console.AssertNoError(err)

	seqSendCmd.InitDefaultHelpCmd()
	seqCmd.AddCommand(seqSendCmd)
}

const feeAmount = 500

func runSeqWithdrawCmd(_ *cobra.Command, args []string) {
	seqID := getSequencerID()
	console.Infof("sequencer ID (source): %s", seqID.String())
	wallet := glb.GetWalletAccount()
	console.Infof("wallet account is: %s", wallet.String())
	targetLock := mustGetTargetAccount()
	console.Infof("amount: %s", util.GoThousands(getAmount()))

	console.Infof("querying wallet's outputs..")
	oData, err := getClient().GetAccountOutputs(wallet)
	console.AssertNoError(err)

	walletOutputs, err := txutils.ParseAndSortOutputData(oData, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		return idx == 0xff
	}, true)
	console.AssertNoError(err)

	console.Infof("will be using fee amount of %d from the wallet. Outputs in the wallet:", feeAmount)
	for i, o := range walletOutputs {
		console.Infof("%d : %s : %s", i, o.ID.Short(), util.GoThousands(o.Output.Amount()))
	}

	if !viper.GetBool("force_send") {
		prompt := fmt.Sprintf("withdraw %s from %s to the target %s?",
			util.GoThousands(getAmount()), seqID.Short(), targetLock.String())
		if !console.YesNoPrompt(prompt, false) {
			console.Infof("exit")
			return
		}
	}

	var amountBin [8]byte
	binary.BigEndian.PutUint64(amountBin[:], getAmount())
	cmdParArr := lazybytes.MakeArrayFromDataReadOnly(targetLock.Bytes(), amountBin[:])
	cmdData := common.Concat(sequencer.CommandCodeWithdrawAmount, cmdParArr)
	constrSource := fmt.Sprintf("concat(0x%s)", hex.EncodeToString(cmdData))
	cmdConstr, err := core.NewGeneralScriptFromSource(constrSource)
	console.AssertNoError(err)

	transferData := txbuilder.NewTransferData(glb.GetPrivateKey(), glb.GetWalletAccount(), core.LogicalTimeNow()).
		WithAmount(feeAmount).
		WithTargetLock(core.ChainLockFromChainID(*seqID)).
		MustWithInputs(walletOutputs...).
		WithSender().
		WithConstraint(cmdConstr)

	txBytes, err := txbuilder.MakeSimpleTransferTransaction(transferData)
	console.AssertNoError(err)

	txStr := transaction.ParseBytesToString(txBytes, func(oid *core.OutputID) ([]byte, bool) {
		idx := slices.IndexFunc(walletOutputs, func(o *core.OutputWithID) bool {
			return o.ID == *oid
		})
		if idx < 0 {
			return nil, false
		}
		return walletOutputs[idx].Output.Bytes(), true
	})

	console.Infof("request transaction:\n%s", txStr)
	console.Infof("submit transaction...")

	err = getClient().SubmitTransaction(txBytes)
	console.AssertNoError(err)
}

func getAmount() uint64 {
	return viper.GetUint64("amount")
}
