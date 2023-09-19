package api

import (
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/spf13/cobra"
)

func initTestSubmitCmd(apiCmd *cobra.Command) {
	testSubmitCmd := &cobra.Command{
		Use:   "test_submit",
		Short: `submits fake tx to the node`,
		Args:  cobra.NoArgs,
		Run:   runTestSubmitCmd,
	}
	testSubmitCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(testSubmitCmd)

}

func runTestSubmitCmd(_ *cobra.Command, args []string) {
	fakeTxBytes := []byte("faketx_faketx_faketx_faketx_faketx_faketx_faketx_")

	err := getClient().SubmitTransaction(fakeTxBytes)
	console.AssertNoError(err)
}
