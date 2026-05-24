package db_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

var (
	numBuckets uint64
	maxRoots   int
)

// Deprecated: use chainstats instead
func initDbStatsCmd() *cobra.Command {
	dbStatsCmd := &cobra.Command{
		Use:   "stats",
		Short: "runs statistics on branches",
		Args:  cobra.NoArgs,
		Run:   runDBStatsCmd,
	}
	dbStatsCmd.PersistentFlags().Uint64VarP(&numBuckets, "buckets", "b", 10, "number of distribution buckets")
	dbStatsCmd.PersistentFlags().IntVarP(&maxRoots, "roots", "r", 2000, "max number of roots to scan")

	dbStatsCmd.InitDefaultHelpCmd()
	return dbStatsCmd
}

func runDBStatsCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	defer glb.CloseDatabases()

	runBranchInflationBonusStats()
}

func runBranchInflationBonusStats() {
	maxInflation := ledger.L(base.MaxSlot).BranchInflationBonusBase(0)
	buckets := make([]int, numBuckets)
	numBranches := 0
	var maxBib, minBib uint64

	multistate.IterateSlotsBack(glb.StateStore(), func(slot uint32, roots []multistate.RootRecord) bool {
		for _, br := range multistate.FetchBranchDataMulti(glb.StateStore(), roots...) {
			bib := br.SequencerOutput.Output.Inflation()

			if bib != 0 {
				bucketNo := bib * numBuckets / maxInflation
				buckets[bucketNo]++
				maxBib = max(maxBib, bib)
				if minBib == 0 {
					minBib = bib
				} else {
					minBib = min(minBib, bib)
				}
			}
			numBranches++
			if numBranches >= maxRoots {
				return false
			}
		}
		return true
	})
	glb.Infof("distribution of branch inflation bonus among %d branch records:\n    minimum: %s\n    maximum: %s\nBuckets:",
		numBranches, util.Th(minBib), util.Th(maxBib))

	for i, n := range buckets {
		glb.Infof("%d: %d (%.1f%%)", i, n, (float64(n)*100)/float64(numBranches))
	}
}
