package snapshot_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func Init() *cobra.Command {
	snapshotCmd := &cobra.Command{
		Use:   "snapshot [<subcommand>]",
		Short: "specifies subcommands for snapshot manipulation",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { _ = cmd.Help() },
	}

	snapshotCmd.PersistentFlags().StringP("config", "c", "", "proxi config profile name")
	err := viper.BindPFlag("config", snapshotCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	snapshotCmd.PersistentFlags().String("api.endpoint", "", "<DNS name>:port endpoint to access the network")
	err = viper.BindPFlag("api.endpoint", snapshotCmd.PersistentFlags().Lookup("api.endpoint"))
	glb.AssertNoError(err)

	snapshotCmd.InitDefaultHelpCmd()
	snapshotCmd.AddCommand(
		initSnapshotDBCmd(),
		initSnapshotInfoCmd(),
		initRestoreCmd(),
		initSnapshotCheckAllCmd(),
		initSnapshotCheckCmd(),
	)
	return snapshotCmd
}
