package server

import (
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/multistate"
)

// calcTxInclusionScore calculates inclusion score response from inclusion data
func (srv *Server) calcTxInclusionScore(inclusion *multistate.TxInclusion, thresholdNumerator, thresholdDenominator int) api.TxInclusionScore {
	srv.Tracef(TraceTagQueryInclusion, "calcTxInclusionScore: %s, threshold: %d/%d", inclusion.String(), thresholdNumerator, thresholdDenominator)

	ret := api.TxInclusionScore{
		ThresholdNumerator:   thresholdNumerator,
		ThresholdDenominator: thresholdDenominator,
		LatestSlot:           int(inclusion.LatestSlot),
		EarliestSlot:         int(inclusion.EarliestSlot),
		StrongScore:          0,
		WeakScore:            0,
	}
	if len(inclusion.Inclusion) == 0 {
		return ret
	}
	var includedInBranches, numDominatingBranches, numIncludedInDominating int
	for i := range inclusion.Inclusion {
		if inclusion.Inclusion[i].Included {
			includedInBranches++
		}
		if inclusion.Inclusion[i].RootRecord.IsCoverageAboveThreshold(thresholdNumerator, thresholdDenominator) {
			numDominatingBranches++
			if inclusion.Inclusion[i].Included {
				numIncludedInDominating++
			}
		}
	}
	ret.WeakScore = (includedInBranches * 100) / len(inclusion.Inclusion)
	if numDominatingBranches > 0 {
		ret.StrongScore = (numIncludedInDominating * 100) / numDominatingBranches
	}
	return ret
}
