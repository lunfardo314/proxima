package util_cmd

import (
	"github.com/spf13/cobra"
)

func keyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "key management commands",
	}
	cmd.AddCommand(
		keyGenerateCmd(),
		keyEncryptCmd(),
		keyDecryptCmd(),
		keyInfoCmd(),
	)
	cmd.InitDefaultHelpCmd()
	return cmd
}
