package init_cmd

import (
	"github.com/spf13/cobra"
)

func CmdInit() *cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init",
		Args:  cobra.NoArgs,
		Short: "various initialization subcommands",
		Run: func(cmd *cobra.Command, args []string) {
		},
	}
	initCmd.AddCommand(
		initGenesisCmd(),
	)
	initCmd.InitDefaultHelpCmd()
	return initCmd
}
