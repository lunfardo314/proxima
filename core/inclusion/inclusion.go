package inclusion

import (
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
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

// InLatestSlot inclusion in the latest slot
func InLatestSlot(inclusionData map[ledger.ChainID]tippool.TxInclusion, thresholdNumerator, thresholdDenominator uint64) (latestSlot ledger.Slot, branchesTotal, numDominatingBranches, numIncludedInDominatingBranches int) {
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
