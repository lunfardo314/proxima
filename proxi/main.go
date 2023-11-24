package main

import (
	"os"
	"strings"

	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/proxi/init_cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func main() {
	glb.Infof("Command line: '%s'", strings.Join(os.Args, " "))

	rootCmd := &cobra.Command{
		Use:   "proxi",
		Short: "a simple CLI for the Proxima project",
		Long: `proxi is a CLI tool for the Proxima project. It provides:
      - initialization of the ledger, node and wallet
      - database level access to the Proxima ledger for admin purposes, including genesis creation
      - access to ledger via the Proxima node API. This includes simple wallet functions
`,
		Run: func(cmd *cobra.Command, _ []string) {
			_ = cmd.Help()
		},
	}

	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbose")
	err := viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))
	glb.AssertNoError(err)

	rootCmd.AddCommand(init_cmd.CmdInit())
	rootCmd.InitDefaultHelpCmd()
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
