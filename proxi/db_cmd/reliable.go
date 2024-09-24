package db_cmd

import (
	"os"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/cobra"
)

func initReliableBranchCmd() *cobra.Command {
	snapshotCmd := &cobra.Command{
		Use:     "reliable_branch",
		Aliases: []string{"lrb"},
		Short:   "finds and displays lates reliable branch in the multi-state DB",
		Args:    cobra.NoArgs,
		Run:     runReliableBranchCmd,
	}

	snapshotCmd.InitDefaultHelpCmd()
	return snapshotCmd
}

func runReliableBranchCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()

	lrb := multistate.FindLatestReliableBranch(glb.StateStore(), global.FractionHealthyBranch)
	if lrb == nil {
		glb.Infof("reliable branch has not been found")
		os.Exit(1)
	}
	nowSlot := ledger.TimeNow().Slot()
	glb.Infof("current slot is %d", nowSlot)
	lrbID := lrb.Stem.ID.TransactionID()
	glb.Infof("latest reliable branch (LRBID) is %s (hex = %s)",
		lrbID.String(), lrbID.StringHex())
	glb.Infof("%d slots back from now. Relative to LRBID, by sequencer ID:", nowSlot-lrbID.Slot())

	byChainID := set.New[ledger.ChainID]()
	counter := 0

	start := time.Now()
	multistate.IterateBranchChainBack(glb.StateStore(), lrb, func(branchID *ledger.TransactionID, branch *multistate.BranchData) bool {
		counter++
		if byChainID.Contains(branch.SequencerID) {
			return true
		}
		byChainID.Insert(branch.SequencerID)

		slotsBefore := int(branchID.Slot()) - int(lrbID.Slot())
		if glb.IsVerbose() {
			glb.Infof("     (%d branches, %d slots) %s %s", 1-counter, slotsBefore, branchID.String(), branch.LinesVerbose().Join("  "))
		} else {
			glb.Infof("     (%d branches, %d slots) %s %s", 1-counter, slotsBefore, branchID.String(), branch.Lines().Join("  "))
		}
		return true
	})
	glb.Infof("--------------------\nTotal %d branches before the LRBID have been scanned in %v", counter-1, time.Since(start))
}
