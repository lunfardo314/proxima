package glb

import (
	"github.com/spf13/cobra"
)

// Init adds all commands in the package to the root command
func Init(rootCmd *cobra.Command) {
	rootCmd.AddCommand(initInitCmd())
	rootCmd.AddCommand(initConfigSetCmd())
	rootCmd.AddCommand(initSetKeyCmd())
}

func initInitCmd() *cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init",
		Args:  cobra.NoArgs,
		Short: "specifies initialization subcommands",
		//Run: func(cmd *cobra.Command, _ []string) {
		//	_ = cmd.Help()
		//},
	}
	initProfileCmd(initCmd)
	initCmd.InitDefaultHelpCmd()
	return initCmd
}
