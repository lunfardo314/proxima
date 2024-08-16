package snapshot_cmd

import (
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initSnapshotInfoCmd() *cobra.Command {
	snapshotInfoCmd := &cobra.Command{
		Use:   "info",
		Short: "reads snapshot file and displays main info",
		Args:  cobra.ExactArgs(1),
		Run:   runSnapshotInfoCmd,
	}

	snapshotInfoCmd.InitDefaultHelpCmd()
	return snapshotInfoCmd
}

func runSnapshotInfoCmd(_ *cobra.Command, args []string) {
	kvStream, err := multistate.OpenSnapshotFileStream(args[0])
	glb.AssertNoError(err)
	kvStream.Close()

	glb.Infof("snapshot file ok. Format version: %s", kvStream.Header.Version)
	glb.Infof("branch ID: %s", kvStream.BranchID.String())
	glb.Infof("root record: %s", kvStream.RootRecord.StringShort())
	glb.Infof("ledger id:\n%s", kvStream.LedgerID.String())
}
