package api

import (
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const defaultAmount = 1_000_000

func initSeqWithdrawCmd(seqCmd *cobra.Command) {
	seqSendCmd := &cobra.Command{
		Use:     "withdraw <target lock>",
		Aliases: util.List("send"),
		Short:   `withdraw tokens from sequencer to the target lock`,
		Args:    cobra.ExactArgs(1),
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

func runSeqWithdrawCmd(_ *cobra.Command, args []string) {
	console.Infof("sequencer ID (source): %s", getSequencerID().String())
	targetLock := mustGetTargetAccount()
	console.Infof("target lock: %s", targetLock.String())
	console.Infof("amount: %s", util.GoThousands(getAmount()))

	if !viper.GetBool("force_send") {
		if !console.YesNoPrompt("send?", false) {
			console.Infof("exit")
			return
		}
	}
	console.Infof("building transaction..")

	//getClient().GetAccountOutputs()

	console.Infof("sending transaction..")

	targetLock = targetLock

	//
	//fakeTxBytes := []byte("faketx_faketx_faketx_faketx_faketx_faketx_faketx_")
	//
	//err := getClient().SubmitTransaction(fakeTxBytes)
	//console.AssertNoError(err)
}

func getAmount() uint64 {
	return viper.GetUint64("amount")
}
