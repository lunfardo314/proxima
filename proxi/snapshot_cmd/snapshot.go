package snapshot_cmd

import (
	"github.com/spf13/cobra"
)

func Init() *cobra.Command {
	dbCmd := &cobra.Command{
		Use:   "snapshot [<subcommand>]",
		Short: "specifies subcommands for snapshot manipulation",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { _ = cmd.Help() },
	}

	dbCmd.InitDefaultHelpCmd()
	dbCmd.AddCommand(
		initSnapshotDBCmd(),
		initSnapshotInfoCmd(),
		initRestoreCmd(),
		initSnapshotCheckCmd(),
	)
	return dbCmd
}
