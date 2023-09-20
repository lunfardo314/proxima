package api

import (
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const defaultAmount = 1_000_000

var (
	sendAmount uint64
)

func initSeqSendCmd(seqCmd *cobra.Command) {
	seqSendCmd := &cobra.Command{
		Use:   "send <target lock>",
		Short: `sends tokens from sequencer to the target lock`,
		Args:  cobra.ExactArgs(1),
		Run:   runSeqSendCmd,
	}

	seqSendCmd.Flags().Uint64P("amount", "a", defaultAmount, "amount to send from sequencer")
	err := viper.BindPFlag("amount", seqSendCmd.Flags().Lookup("amount"))
	console.AssertNoError(err)

	seqSendCmd.Flags().BoolP("force_send", "f", false, "force send")
	err = viper.BindPFlag("force_send", seqSendCmd.Flags().Lookup("force_send"))
	console.AssertNoError(err)

	seqSendCmd.InitDefaultHelpCmd()
	seqCmd.AddCommand(seqSendCmd)
}

func runSeqSendCmd(_ *cobra.Command, args []string) {
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
