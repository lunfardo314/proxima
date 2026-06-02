package config_cmd

import (
	"github.com/spf13/cobra"
)

func CmdConfig() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Args:  cobra.NoArgs,
		Short: "wallet and node configuration subcommands",
		Run: func(cmd *cobra.Command, args []string) {
		},
	}
	configCmd.AddCommand(
		configWalletCmd(),
		configNodeCmd(),
	)
	configCmd.InitDefaultHelpCmd()
	return configCmd
}
