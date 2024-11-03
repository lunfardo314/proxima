package snapshot_cmd

import (
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initSnapshotCheckCmd() *cobra.Command {
	snapshotCheckCmd := &cobra.Command{
		Use:   "check [<snapshot file name>]",
		Short: "reads snapshot file and checks if branch is part opf the LRB on the node",
		Args:  cobra.MaximumNArgs(1),
		Run:   runSnapshotCheckCmd,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
	}

	snapshotCheckCmd.PersistentFlags().StringP("config", "c", "", "proxi config profile name")
	err := viper.BindPFlag("config", snapshotCheckCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	snapshotCheckCmd.PersistentFlags().String("api.endpoint", "", "<DNS name>:port")
	err = viper.BindPFlag("api.endpoint", snapshotCheckCmd.PersistentFlags().Lookup("api.endpoint"))
	glb.AssertNoError(err)

	snapshotCheckCmd.InitDefaultHelpCmd()
	return snapshotCheckCmd
}

func runSnapshotCheckCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	clnt := glb.GetClient()

	var fname string
	var ok bool
	if len(args) == 0 {
		fname, ok = findLatestSnapshotFile()
		glb.Assertf(ok, "can't find snapshot file")
	} else {
		fname = args[0]
	}

	glb.Infof("reading snapshot file %s", fname)
	kvStream, err := multistate.OpenSnapshotFileStream(fname)
	glb.AssertNoError(err)
	defer kvStream.Close()

	glb.Infof("snapshot format version: %s", kvStream.Header.Version)
	glb.Infof("snapshot branch ID: %s", kvStream.BranchID.String())
	glb.Infof("snapshot root record:\n%s", kvStream.RootRecord.Lines("    ").String())

	lrbID, included, err := clnt.CheckTransactionIDInLRB(kvStream.BranchID)
	glb.AssertNoError(err)
	glb.Infof("\n-----------------------\n latest reliable branch: %s", lrbID.String())
	if included {
		glb.Infof("the snapshot is INCLUDED in the current LRB of the network. It CAN BE USED to start a node")
	} else {
		glb.Infof("the snapshot is NOT INCLUDED in the current LRB of the network. It CANNOT BE USED used to start a node")
	}
}
