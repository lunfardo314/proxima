package db_cmd

import (
	"os"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initReliableBranchCmd() *cobra.Command {
	snapshotCmd := &cobra.Command{
		Use:   "reliable_branch",
		Short: "finds and displays lates reliable branch in the multi-state DB",
		Args:  cobra.NoArgs,
		Run:   runReliableBranchCmd,
	}

	snapshotCmd.InitDefaultHelpCmd()
	return snapshotCmd
}

func runReliableBranchCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()

	latestReliableBranch, found := multistate.FindLatestReliableBranch(glb.StateStore(), global.FractionHealthyBranch)
	if !found {
		glb.Infof("reliable branch has not been found")
		os.Exit(1)
	}
	nowSlot := ledger.TimeNow().Slot()
	glb.Infof("current slot is %d", nowSlot)
	branchID := latestReliableBranch.Stem.ID.TransactionID()
	glb.Infof("latest reliable branch is %s (hex = %s)",
		branchID.String(), branchID.StringHex())
	glb.Infof("%s slots back", nowSlot-branchID.Slot())
}
