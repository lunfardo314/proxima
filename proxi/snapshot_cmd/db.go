package snapshot_cmd

import (
	"context"

	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initSnapshotDBCmd() *cobra.Command {
	snapshotCmd := &cobra.Command{
		Use:   "db",
		Short: "writes state snapshot to file",
		Args:  cobra.NoArgs,
		Run:   runSnapshotCmd,
	}

	snapshotCmd.InitDefaultHelpCmd()
	return snapshotCmd
}

func runSnapshotCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	rootRecord, fname, err := multistate.SaveSnapshot(glb.StateStore(), context.Background())
	glb.AssertNoError(err)

	glb.Infof("latest reliable state has been saved to the snapshot file %s", fname)
	glb.Infof("root record:\n%s", rootRecord.StringShort())
}
