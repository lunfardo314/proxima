package vertex

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

// AlignedCoverages shifts one of coverages, if necessary, so that to make them comparable
func AlignedCoverages(vid1, vid2 *WrappedTx) (uint64, uint64) {
	lc1 := vid1.GetLedgerCoverageP()
	util.Assertf(lc1 != nil, "coverage not set in %s", vid1.IDShortString)
	lc2 := vid2.GetLedgerCoverageP()
	util.Assertf(lc2 != nil, "coverage not set in %s", vid2.IDShortString)

	if vid1.Timestamp() == vid2.Timestamp() {
		// same time -> coverages are comparable
		return *lc1, *lc2
	}
	// to simplify logic, make one before another
	swapped := false
	v1, v2 := vid1, vid2
	if v1.Timestamp().After(v2.Timestamp()) {
		v1, v2 = v2, v1
		lc1, lc2 = lc2, lc1
		swapped = true
	}
	// now v1 is strongly before v2
	util.Assertf(v1.Timestamp().Before(v2.Timestamp()), "v1.Timestamp().Before(v2.Timestamp())")
	lc1Ret := *lc1
	lc2Ret := *lc2
	if v1.IsBranchTransaction() || v1.Slot() != v2.Slot() {
		lc1Ret >>= int(v2.Slot()-v1.Slot()) + 1 // ??? what about 2 non-branches
	}
	if swapped {
		return lc2Ret, lc1Ret
	}
	return lc1Ret, lc2Ret
}

// IsPreferredMilestoneAgainstTheOther returns if vid1 is strongly better than vid2
func IsPreferredMilestoneAgainstTheOther(vid1, vid2 *WrappedTx, preferYounger bool) bool {
	util.Assertf(vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone(), "vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone()")
	if vid1 == vid2 {
		return false
	}
	lc1, lc2 := AlignedCoverages(vid1, vid2)
	slc1, slc2 := lc1, lc2
	if slc1 != slc2 {
		return slc1 > slc2
	}
	// equal coverage sums, compare IDs
	if ledger.LessTxID(vid1.ID, vid2.ID) {
		return preferYounger
	}
	return !preferYounger
}
