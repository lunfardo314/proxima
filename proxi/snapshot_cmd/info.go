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
	header, id, branchID, rootRecord, kvStream, err := multistate.OpenSnapshotFileStream(args[0])
	glb.AssertNoError(err)
	glb.Infof("snapshot file ok. Format version: %s", header.Version)
	glb.Infof("branch ID: %s", branchID.String())
	glb.Infof("root record: %s", rootRecord.StringShort())
	glb.Infof("ledger id:\n%s", id.String())
	err = kvStream.Close()
	glb.AssertNoError(err)
}
