package util_cmd

import (
	"github.com/spf13/cobra"
)

func Init() *cobra.Command {
	genCmd := &cobra.Command{
		Use:   "util",
		Args:  cobra.NoArgs,
		Short: "utility functions",
		Run: func(cmd *cobra.Command, args []string) {
		},
	}
	genCmd.AddCommand(
		genHostIDCmd(),
		genIDCmd(),
		verifyIDCmd(),
		compileIDCmd(),
		initParseTx(),
		initParseBytecode(),
		initDecodeMsgCmd(),
		initInflationCmd(),
		keyCmd(),
	)
	genCmd.InitDefaultHelpCmd()
	return genCmd
}
