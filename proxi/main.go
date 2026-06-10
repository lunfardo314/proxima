package main

import (
	"os"
	"strings"

	"github.com/lunfardo314/proxima/proxi/config_cmd"
	"github.com/lunfardo314/proxima/proxi/db_cmd"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/proxi/init_cmd"
	"github.com/lunfardo314/proxima/proxi/node_cmd"
	"github.com/lunfardo314/proxima/proxi/snapshot_cmd"
	"github.com/lunfardo314/proxima/proxi/util_cmd"
	"github.com/lunfardo314/proxima/proxi/version"
	"github.com/lunfardo314/proxima/proxi/wallet_cmd"
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
      - database level access to the Proxima ledger for admin purposes, including genesis creation and snapshots
      - access to ledger via the Proxima node API. This includes simple wallet functions to access usual accounts 
and withdraw funds from the sequencer chain
`,
		Run: func(cmd *cobra.Command, _ []string) {
			_ = cmd.Help()
		},
	}

	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbosity level 1")
	err := viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))
	glb.AssertNoError(err)

	rootCmd.PersistentFlags().BoolP("v2", "2", false, "verbosity level 2")
	err = viper.BindPFlag("v2", rootCmd.PersistentFlags().Lookup("v2"))
	glb.AssertNoError(err)

	rootCmd.PersistentFlags().BoolP("force", "f", false, "override yes/no prompt")
	err = viper.BindPFlag("force", rootCmd.PersistentFlags().Lookup("force"))
	glb.AssertNoError(err)

	// 'config' is bound once here at the root so it inherits to every subcommand.
	// Binding it per-subcommand (as was done before) is broken: viper keeps only
	// the last BindPFlag for a given key in its global state, so the value parsed
	// onto one subcommand's flag object would never be the one viper reads back.
	rootCmd.PersistentFlags().StringP("config", "c", "", "proxi config profile name")
	err = viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	rootCmd.AddCommand(
		config_cmd.CmdConfig(),
		init_cmd.CmdInit(),
		db_cmd.Init(),
		node_cmd.Init(),
		util_cmd.Init(),
		snapshot_cmd.Init(),
		version.CmdVersion(),
		wallet_cmd.InitWalletCmd(),
	)
	rootCmd.InitDefaultHelpCmd()
	if err = rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
