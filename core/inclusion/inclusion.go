package inclusion

import (
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// InSlot returns TxInclusion data by chain ID for branches with the coverage above threshold thresholdNumerator/thresholdDenominator
func InSlot(inclusionData map[ledger.ChainID]tippool.TxInclusion, slot ledger.Slot, thresholdNumerator, thresholdDenominator uint64) (branchesTotal, numDominatingBranches, numIncludedInDominatingBranches int) {
	util.Assertf(multistate.ValidInclusionThresholdFraction(thresholdNumerator, thresholdDenominator), "inclusion.InSlot: threshold fraction is wrong")
	for _, incl := range inclusionData {
		if incl.BranchID.Slot() != slot {
			continue
		}
		branchesTotal++
		if incl.RootRecord.IsCoverageAboveThreshold(thresholdNumerator, thresholdDenominator) {
			numDominatingBranches++
			if incl.Included {
				if incl.RootRecord.IsCoverageAboveThreshold(thresholdNumerator, thresholdDenominator) {
					numIncludedInDominatingBranches++
				}
			}
		}
	}
	return
}

// Totals inclusion in the latest slot
func Totals(inclusionData map[ledger.ChainID]tippool.TxInclusion, thresholdNumerator, thresholdDenominator uint64) (latestSlot ledger.Slot, branchesTotal, numDominatingBranches, numIncludedInDominatingBranches int) {
	if len(inclusionData) == 0 {
		return
	}
	for _, incl := range inclusionData {
		if incl.BranchID.Slot() > latestSlot {
			latestSlot = incl.BranchID.Slot()
		}
	}
	branchesTotal, numDominatingBranches, numIncludedInDominatingBranches = InSlot(inclusionData, latestSlot, thresholdNumerator, thresholdDenominator)
	return
}

func Lines(inclusionData map[ledger.ChainID]tippool.TxInclusion, thresholdNumerator, thresholdDenominator uint64, prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for chainID, incl := range inclusionData {
		i := "-"
		if incl.Included {
			i = "+"
		}
		above := incl.RootRecord.IsCoverageAboveThreshold(thresholdNumerator, thresholdDenominator)
		ret.Add("%s %s: %s (thr: %v)", i, chainID.StringShort(), util.GoTh(incl.RootRecord.LedgerCoverage), above)
	}
	return ret
}
