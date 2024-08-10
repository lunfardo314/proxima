package db_cmd

import (
	"github.com/lunfardo314/proxima/proxi/db_cmd/txstore"
	"github.com/spf13/cobra"
)

func Init() *cobra.Command {
	dbCmd := &cobra.Command{
		Use:   "db [<subcommand>]",
		Short: "specifies subcommand on the database",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { _ = cmd.Help() },
	}

	dbCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(
		initDBInfoCmd(),
		initDBTreeCmd(),
		initDBDAGCmd(),
		initMainChainCmd(),
		initAccountsCmd(),
		initBranchesCmd(),
		initSnapshotCmd(),
		txstore.Init(),
	)
	return dbCmd
}
