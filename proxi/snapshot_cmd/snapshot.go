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
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			glb.BindNodeAPIFlags(cmd)
		},
		Run: func(cmd *cobra.Command, _ []string) { _ = cmd.Help() },
	}

	// 'config' / '-c' is a persistent flag on the root command (see proxi/main.go),
	// inherited here; do not re-bind it per subcommand.

	snapshotCmd.PersistentFlags().String("api.node_url", "", "URL of the node API, e.g. http://127.0.0.1:8000")
	err := viper.BindPFlag("api.node_url", snapshotCmd.PersistentFlags().Lookup("api.node_url"))
	glb.AssertNoError(err)

	snapshotCmd.PersistentFlags().String("api.endpoint", "", "legacy name of --api.node_url")
	glb.AssertNoError(snapshotCmd.PersistentFlags().MarkHidden("api.endpoint"))
	err = viper.BindPFlag("api.endpoint", snapshotCmd.PersistentFlags().Lookup("api.endpoint"))
	glb.AssertNoError(err)

	snapshotCmd.InitDefaultHelpCmd()
	snapshotCmd.AddCommand(
		initSnapshotDBCmd(),
		initSnapshotInfoCmd(),
		initRestoreCmd(),
		initSnapshotCheckCmd(),
	)
	return snapshotCmd
}
