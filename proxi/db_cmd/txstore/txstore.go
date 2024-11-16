package txstore

import "github.com/spf13/cobra"

func Init() *cobra.Command {
	dbCmd := &cobra.Command{
		Use:   "txstore [<subcommand>]",
		Short: "specifies subcommands on txStore DB",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { _ = cmd.Help() },
	}

	dbCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(
		initCrossCheckCmd(),
		initGetCmd(),
		initListCmd(),
		initPutCmd(),
		initPastConeCmd(),
	)
	return dbCmd
}
