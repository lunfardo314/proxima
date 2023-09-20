package api

import (
	"github.com/lunfardo314/proxima/api/client"
	api "github.com/lunfardo314/proxima/proxi/api/seq"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	serverEndpoint string
)

func Init(rootCmd *cobra.Command) {
	apiCmd := &cobra.Command{
		Use:   "api [<subcommand>]",
		Short: "specifies node api subcommand",
		Args:  cobra.MaximumNArgs(1),
	}

	apiCmd.PersistentFlags().StringVar(&serverEndpoint, "api.endpoint", "", "<DNS name>:port")
	err := viper.BindPFlag("api.endpoint", apiCmd.PersistentFlags().Lookup("api.endpoint"))
	console.AssertNoError(err)

	apiCmd.PersistentFlags().StringP("target", "t", "", "target account as an EasyFl source of the accountable constraint")
	err = viper.BindPFlag("target", apiCmd.PersistentFlags().Lookup("target"))
	console.AssertNoError(err)

	apiCmd.InitDefaultHelpCmd()
	initGetOutputsCmd(apiCmd)
	initGetUTXOCmd(apiCmd)
	initGetChainOutputCmd(apiCmd)
	initTestSubmitCmd(apiCmd)
	api.Init(apiCmd)

	rootCmd.AddCommand(apiCmd)
}

func getClient() *client.APIClient {
	return client.New(viper.GetString("api.endpoint"))
}
