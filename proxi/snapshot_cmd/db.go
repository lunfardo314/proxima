package snapshot_cmd

import (
	"context"
	"io"
	"os"

	"github.com/lunfardo314/proxima/global"
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

	console := io.Discard
	if glb.IsVerbose() {
		console = os.Stdout
	}
	snapshotBranch := multistate.FindLatestReliableBranchAndNSlotsBack(glb.StateStore(), 10, global.FractionHealthyBranch)
	glb.Assertf(snapshotBranch != nil, "can't find latest reliable branch")
	fname, stats, err := multistate.SaveSnapshot(glb.StateStore(), snapshotBranch, context.Background(), "", console)
	glb.AssertNoError(err)

	glb.Infof("latest reliable state has been saved to the snapshot file %s", fname)
	glb.Infof("branch data:\n%s", snapshotBranch.LinesVerbose("   ").String())
	glb.Infof("%s", stats.Lines("     ").String())
}
