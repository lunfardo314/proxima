package init_cmd

import (
	"github.com/spf13/cobra"
)

func CmdInit() *cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init",
		Args:  cobra.NoArgs,
		Short: "specifies initialization subcommands",
		Run: func(cmd *cobra.Command, args []string) {
		},
	}
	initCmd.AddCommand(
		initProfileCmd(),
		initIDCmd(),
		initGenesisDBCmd(),
		initBootstrapAccountCmd(),
		initNodeConfigCmd(),
	)
	initCmd.InitDefaultHelpCmd()
	return initCmd
}
