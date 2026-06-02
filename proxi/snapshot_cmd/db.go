package snapshot_cmd

import (
	"context"
	"io"
	"os"
	"strconv"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initSnapshotDBCmd() *cobra.Command {
	snapshotCmd := &cobra.Command{
		Use:   "db [<slots back from LRB, default is 10>]",
		Short: "writes state snapshot to file",
		Args:  cobra.MaximumNArgs(1),
		Run:   runSnapshotCmd,
	}

	snapshotCmd.InitDefaultHelpCmd()
	return snapshotCmd
}

const defaultSlotsBackFromLRB = 10

func runSnapshotCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	console := io.Discard
	if glb.IsVerbose() {
		console = os.Stdout
	}

	slotsBackFromLRB := defaultSlotsBackFromLRB
	if len(args) > 0 {
		var err error
		slotsBackFromLRB, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
	}

	snapshotBranch := multistate.FindLatestReliableBranchAndNSlotsBack(glb.StateStore(), slotsBackFromLRB, global.FractionHealthyBranch())
	glb.Assertf(snapshotBranch != nil, "can't find latest reliable branch")
	fname, stats, err := multistate.SaveSnapshot(glb.StateStore(), snapshotBranch, context.Background(), "", console)
	glb.AssertNoError(err)

	glb.Infof("latest reliable state has been saved to the snapshot file %s", fname)
	glb.Infof("branch data:\n%s", snapshotBranch.LinesVerbose("   ").String())
	glb.Infof("%s", stats.Lines("     ").String())
}
