package db_cmd

import (
	"sort"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

var (
	_numBuckets uint64
	_maxRoots   int
)

func initDbChainStatsCmd() *cobra.Command {
	dbStatsCmd := &cobra.Command{
		Use:   "chainstats",
		Short: "runs statistics on branches in the main chain",
		Args:  cobra.NoArgs,
		Run:   runDBChainStatsCmd,
	}
	dbStatsCmd.PersistentFlags().Uint64VarP(&_numBuckets, "buckets", "b", 10, "number of distribution buckets")
	dbStatsCmd.PersistentFlags().IntVarP(&_maxRoots, "roots", "r", 2000, "max number of roots to scan")

	dbStatsCmd.InitDefaultHelpCmd()
	return dbStatsCmd
}

func runDBChainStatsCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	runChainStats()
}

type (
	seqStats struct {
		numBranches  int
		sumInflation uint64
		minBalance   uint64
		maxBalance   uint64
		minSlot      int
		maxSlot      int
	}

	winningBranch struct {
		score int
		outOf int
	}
)

func runChainStats() {
	lib := ledger.L(base.MaxSlot)
	maxInflation := lib.BranchInflationBonusBase(0)
	buckets := make([]int, _numBuckets)
	sequencers := make(map[base.ChainID]*seqStats)
	allBibs := make([]uint64, 0, 100000)
	numBranches := 0
	var maxBib, minBib uint64

	lrb := multistate.FindLatestReliableBranch(glb.StateStore(), global.FractionHealthyBranch())
	glb.Assertf(lrb != nil, "no latest reliable branch found")

	chainBranches := make(map[base.TransactionID]winningBranch)
	bibSum := uint64(0)
	multistate.IterateBranchChainBack(glb.StateStore(), lrb, func(branchID *base.TransactionID, br *multistate.BranchData) bool {
		chainBranches[br.TxID()] = winningBranch{}

		bib := br.SequencerOutput.Output.Inflation()
		numBranches++
		bibSum += bib
		allBibs = append(allBibs, bib)

		if bib == 0 {
			return true
		}

		// check consistency
		stemConstraint, ok := br.Stem.Output.StemLock()
		glb.Assertf(ok, "stem lock not found in %s hex=%s", br.Stem.ID.String(), br.Stem.ID.StringHex())

		bibCalc := lib.BranchInflationBonus(stemConstraint.VRFProof, br.Slot())
		glb.Assertf(bib == bibCalc, "provided vs calculated inflation mismatch %s != %s in %s",
			util.Th(bib), util.Th(bibCalc), br.Lines("        ").String())
		bibDirect := lib.BranchInflationBonus(stemConstraint.VRFProof, br.Slot())
		glb.Assertf(bib == bibDirect, "provided vs directly calculated inflation mismatch: %s != %s in %s",
			util.Th(bib), util.Th(bibDirect), br.Lines("        ").String())
		bucketNo := bib * _numBuckets / maxInflation
		buckets[bucketNo]++
		maxBib = max(maxBib, bib)
		if minBib == 0 {
			minBib = bib
		} else {
			minBib = min(minBib, bib)
		}

		seqID, ok := br.SequencerOutput.ExtractChainID()
		glb.Assertf(ok, "failed to extract chain id from %s", br.Lines("        ").String())

		seqStatsRec, ok := sequencers[seqID]
		if !ok {
			seqStatsRec = &seqStats{}
			sequencers[seqID] = seqStatsRec
		}
		seqStatsRec.numBranches++
		seqStatsRec.sumInflation += bib
		if seqStatsRec.minBalance == 0 {
			seqStatsRec.minBalance = br.SequencerOutput.Output.TokenBalance()
		} else {
			seqStatsRec.minBalance = min(br.SequencerOutput.Output.TokenBalance(), seqStatsRec.minBalance)
		}
		seqStatsRec.maxBalance = max(br.SequencerOutput.Output.TokenBalance(), seqStatsRec.maxBalance)
		if seqStatsRec.minSlot == 0 {
			seqStatsRec.minSlot = int(br.SequencerOutput.ID.Slot())
		} else {
			seqStatsRec.minSlot = min(int(br.SequencerOutput.ID.Slot()), seqStatsRec.minSlot)
		}
		seqStatsRec.maxSlot = max(int(br.SequencerOutput.ID.Slot()), seqStatsRec.maxSlot)

		if numBranches >= _maxRoots {
			return false
		}
		return true
	})

	glb.Infof("\ndistribution of branch inflation bonus among %d branch records in the main chain:\n    minimum: %s\n    maximum: %s\n    avg:     %s\n    median:  %s\nBy buckets:",
		numBranches, util.Th(minBib), util.Th(maxBib), util.Th(bibSum/uint64(numBranches)), util.Th(util.Median(allBibs)))

	for i, n := range buckets {
		glb.Infof("%d: %d (%.1f%%)", i, n, (float64(n)*100)/float64(numBranches))
	}
	glb.Infof("\nstats by sequencer:")
	seqIDs := util.KeysSorted(sequencers, func(id1, id2 base.ChainID) bool {
		return sequencers[id1].numBranches > sequencers[id2].numBranches
	})

	for _, seqID := range seqIDs {
		seqStatsRec := sequencers[seqID]
		glb.Infof("   %s  %6d branches (%.1f%%), %6d slots, %d per 100 slots, avg BIB: %s, balance: min = %s  max = %s",
			seqID.String(),
			seqStatsRec.numBranches,
			float64(seqStatsRec.numBranches)*100/float64(numBranches),
			seqStatsRec.maxSlot-seqStatsRec.minSlot+1,
			seqStatsRec.numBranches*100/(seqStatsRec.maxSlot-seqStatsRec.minSlot+1),
			util.Th(seqStatsRec.sumInflation/uint64(seqStatsRec.numBranches)),
			util.Th(seqStatsRec.minBalance),
			util.Th(seqStatsRec.maxBalance),
		)
	}

	glb.Infof("\nwinning branch by BIB score in the slot:")

	numBranches = 0
	maxBranchesInSlot := 0
	multistate.IterateSlotsBack(glb.StateStore(), func(slot uint32, roots []multistate.RootRecord) bool {
		branches := multistate.FetchBranchDataMulti(glb.StateStore(), roots...)
		// sort by inflation descending
		if len(branches) == 0 {
			return true
		}
		numBranches += 0
		sort.Slice(branches, func(i, j int) bool {
			return branches[i].SequencerOutput.Output.Inflation() > branches[j].SequencerOutput.Output.Inflation()
		})

		var winningTxID *base.TransactionID
		var idx int
		for i, br := range branches {
			if _, ok := chainBranches[br.TxID()]; ok {
				// it is a winning branch
				winningTxID = util.Ref(br.TxID())
				idx = i
			}
		}
		if winningTxID != nil {
			chainBranches[*winningTxID] = winningBranch{
				score: idx,
				outOf: len(branches),
			}
		}
		maxBranchesInSlot = max(maxBranchesInSlot, len(branches))
		if numBranches >= _maxRoots {
			return false
		}
		return true
	})

	branchIDs := util.KeysSorted(chainBranches, func(id1, id2 base.TransactionID) bool {
		return id1.Slot() < id2.Slot()
	})
	buckets1 := make([]int, maxBranchesInSlot)
	for _, branchID := range branchIDs {
		//glb.Infof("   %s  %d / %d", branchID.String(), chainBranches[branchID].score, chainBranches[branchID].outOf)
		buckets1[chainBranches[branchID].score]++
	}

	glb.Infof("by buckets:\n")
	for i, no := range buckets1 {
		glb.Infof("  bucket #%d: %d (%.1f%%)", i, no, (float64(no)*100)/float64(len(branchIDs)))
	}

}
