package db_cmd

import (
	"github.com/spf13/cobra"
)

func Init() *cobra.Command {
	dbCmd := &cobra.Command{
		Use:   "db [<subcommand>]",
		Short: "specifies subcommand on the database",
		Args:  cobra.NoArgs,
		Run:   func(_ *cobra.Command, _ []string) {},
	}

	dbCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(
		initDBInfoCmd(),
		initDBTreeCmd(),
		initDBDAGCmd(),
		initMainChainCmd(),
		initAccountsCmd(),
	)
	return dbCmd
}
