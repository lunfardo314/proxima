package db_cmd

import (
	"os"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/set"
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
	glb.Infof("latest reliable branch (LRB) is %s (hex = %s)",
		branchID.String(), branchID.StringHex())
	glb.Infof("%d slots back from now", nowSlot-branchID.Slot())

	byChainID := set.New[ledger.ChainID]()
	slotsBackFromLatest := 0
	counter := 0

	multistate.IterateBranchChainBack(glb.StateStore(), latestReliableBranch, func(branchID *ledger.TransactionID, branch *multistate.BranchData) bool {
		counter++
		if byChainID.Contains(branch.SequencerID) {
			return true
		}
		byChainID.Insert(branch.SequencerID)

		if glb.IsVerbose() {
			glb.Infof("     (%d) %s %s", slotsBackFromLatest, branchID.String(), branch.LinesVerbose().Join("  "))
		} else {
			glb.Infof("     (%d) %s %s", slotsBackFromLatest, branchID.String(), branch.Lines().Join("  "))
		}
		slotsBackFromLatest--
		return true
	})
	glb.Infof("--------------------\nTotal branches before the LRB scanned: %d", counter-1)
}
