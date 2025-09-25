package db_cmd

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/cobra"
)

var (
	slotsBackAnalyzeBranches int
	missingOnly              bool
)

func initAnalyzeBranchesCmd() *cobra.Command {
	dbMainChainCmd := &cobra.Command{
		Use:   "analyze_branches",
		Short: "scans slots back, displays main chain plus some info about the branches in the slot",
		Args:  cobra.MaximumNArgs(1),
		Run:   runAnalyzeBranchesCmd,
	}
	dbMainChainCmd.PersistentFlags().IntVarP(&slotsBackAnalyzeBranches, "slots_back", "s", 1000, "limit maximum how many slots back")
	dbMainChainCmd.PersistentFlags().BoolVarP(&missingOnly, "missing_only", "m", false, "display only branches with missing sequencers")

	dbMainChainCmd.InitDefaultHelpCmd()
	return dbMainChainCmd
}

func runAnalyzeBranchesCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	latest := multistate.FetchLatestCommittedSlot(glb.StateStore())
	glb.Infof("latest committed slot: %d\n", latest)

	latestBranches := multistate.FetchLatestBranches(glb.StateStore())
	tip := util.Maximum(latestBranches, func(br1, br2 *multistate.BranchData) bool {
		return br1.CoverageDelta < br2.CoverageDelta
	})

	mainChain := make([]*multistate.BranchData, 0)
	allKnownSeqIDs := set.New[base.ChainID]()
	multistate.IterateBranchChainBack(glb.StateStore(), tip, func(branchID *base.TransactionID, branch *multistate.BranchData) bool {
		mainChain = append(mainChain, branch)
		allKnownSeqIDs.Insert(branch.SequencerID)
		return slotsBackAnalyzeBranches <= 0 || len(mainChain) <= slotsBackAnalyzeBranches
	})

	countWithMissing := 0
	for _, br := range mainChain {
		rootsInTheSlot := multistate.FetchRootRecords(glb.StateStore(), br.Slot())
		glb.Assertf(len(rootsInTheSlot) > 0, "len(rootsInTheSlot)>0")

		missingSeqIDs := allKnownSeqIDs.Clone()
		for _, root := range rootsInTheSlot {
			missingSeqIDs.Remove(root.SequencerID)
		}
		if len(missingSeqIDs) == 0 && missingOnly {
			continue
		}
		countWithMissing++
		ln := missingSeqIDs.Lines(func(seqID base.ChainID) string {
			return seqID.StringShort()
		})
		glb.Infof("%6d: main: %s (%s), seqs that didn't produce a branch in the slot: [%s]", br.Slot(), br.SequencerID.StringShort(), util.Th(br.CoverageDelta), ln.Join(", "))
	}
	glb.Infof("total slots analyzed: %d", len(mainChain))
	glb.Infof("total slots with some sequencers missing branches: %d", countWithMissing)
}
