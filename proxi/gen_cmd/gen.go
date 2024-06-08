package gen_cmd

import (
	"github.com/spf13/cobra"
)

func Init() *cobra.Command {
	genCmd := &cobra.Command{
		Use:   "gen",
		Args:  cobra.NoArgs,
		Short: "utility data generation functions",
		Run: func(cmd *cobra.Command, args []string) {
		},
	}
	genCmd.AddCommand(
		genEd25519Cmd(),
		genHostIDCmd(),
	)
	genCmd.InitDefaultHelpCmd()
	return genCmd
}
