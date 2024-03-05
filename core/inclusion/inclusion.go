package inclusion

import (
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

type InSlotInclusionData struct {
	numBranchesTotal      int
	numBranchesDominating int
	numIncludedTotal      int
	numIncludedDominating int
}

func InSlot(inclusionData map[ledger.ChainID]tippool.TxInclusion, slot ledger.Slot) (ret InSlotInclusionData) {
	for _, incl := range inclusionData {
		if incl.BranchID.Slot() != slot {
			continue
		}
		ret.numBranchesTotal++
		if incl.Included {
			ret.numIncludedTotal++
		}
		if incl.RootRecord.IsDominating() {
			ret.numBranchesDominating++
			if incl.Included {
				ret.numIncludedDominating++
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
func (i *InSlotInclusionData) Score() (retTotal, retDominating int) {
	util.Assertf(i.numBranchesTotal >= i.numBranchesDominating, "i.numBranchesTotal >= i.numBranchesDominating")
	util.Assertf(i.numIncludedTotal >= i.numIncludedDominating, "i.numIncludedTotal >= i.numIncludedDominating")
	nT, nD := 0, 0
	if i.numBranchesTotal > 0 {
		nT = (i.numIncludedTotal * 100) / i.numBranchesTotal
	}
	if i.numBranchesDominating > 0 {
		nT = (i.numIncludedDominating * 100) / i.numBranchesDominating
	}
	return nT, nD
}

func ScoreLatestSlot(inclusionData map[ledger.ChainID]tippool.TxInclusion) (retTotal, retDominating int) {
	_, ret := InLatestSlot(inclusionData)
	return ret.Score()
}
