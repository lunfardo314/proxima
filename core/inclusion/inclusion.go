package inclusion

import (
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

type InSlotInclusionData struct {
	NumBranchesTotal      int
	NumBranchesDominating int
	NumIncludedTotal      int
	NumIncludedDominating int
}

func InSlot(inclusionData map[ledger.ChainID]tippool.TxInclusion, slot ledger.Slot) (ret InSlotInclusionData) {
	for _, incl := range inclusionData {
		if incl.BranchID.Slot() != slot {
			continue
		}
		ret.NumBranchesTotal++
		if incl.Included {
			ret.NumIncludedTotal++
		}
		if incl.RootRecord.IsDominating() {
			ret.NumBranchesDominating++
			if incl.Included {
				ret.NumIncludedDominating++
			}
		}
	}
	return
}

func InLatestSlot(inclusionData map[ledger.ChainID]tippool.TxInclusion) (retSlot ledger.Slot, retInclusion InSlotInclusionData) {
	if len(inclusionData) == 0 {
		return
	}
	latestSlot := ledger.Slot(0)
	for _, incl := range inclusionData {
		if incl.BranchID.Slot() > latestSlot {
			latestSlot = incl.BranchID.Slot()
		}
	}
	return latestSlot, InSlot(inclusionData, latestSlot)
}

// Score calculates simple inclusion score for the slot
func (i *InSlotInclusionData) Score() (retPercentTotal, retPercentDominating int) {
	util.Assertf(i.NumBranchesTotal >= i.NumBranchesDominating, "i.NumBranchesTotal >= i.NumBranchesDominating")
	util.Assertf(i.NumIncludedTotal >= i.NumIncludedDominating, "i.NumIncludedTotal >= i.NumIncludedDominating")
	nT, nD := 0, 0
	if i.NumBranchesTotal > 0 {
		nT = (i.NumIncludedTotal * 100) / i.NumBranchesTotal
	}
	if i.NumBranchesDominating > 0 {
		nT = (i.NumIncludedDominating * 100) / i.NumBranchesDominating
	}
	return nT, nD
}

func ScoreLatestSlot(inclusionData map[ledger.ChainID]tippool.TxInclusion) (retPercentTotal, retPercentDominating int) {
	_, incl := InLatestSlot(inclusionData)
	return incl.Score()
}
