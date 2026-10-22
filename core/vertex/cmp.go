package vertex

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// IsPreferredMilestoneAgainstTheOther returns if vid1 is strongly better than vid2: bigger
// ledger coverage; between two branches of equal coverage, the bigger branch inflation bonus;
// otherwise the transaction ID, only to keep the order total.
//
// Competing branches of a slot tie on coverage as a rule, and every sequencer must break the
// tie the same way. The bonus is a VRF output of the issuer's key and the slot, which nobody
// can choose; a branch's transaction ID its issuer can grind by varying the branch.
func IsPreferredMilestoneAgainstTheOther(vid1, vid2 *WrappedTx) bool {
	util.Assertf(vid1.IsSequencerTransaction() && vid2.IsSequencerTransaction(), "vid1.IsSequencerTransaction() && vid2.IsSequencerTransaction()")
	if vid1 == vid2 {
		return false
	}
	lc1 := vid1.GetLedgerCoverageP()
	lc2 := vid2.GetLedgerCoverageP()
	if lc1 == nil {
		return false
	}
	if lc2 == nil {
		return true
	}
	if *lc1 > *lc2 {
		return true
	}
	if *lc1 == *lc2 {
		if vid1.IsBranchTransaction() && vid2.IsBranchTransaction() {
			if b1, b2 := vid1.InflationAmount(), vid2.InflationAmount(); b1 != b2 {
				return b1 > b2
			}
		}
		return base.LessTxID(vid2.id, vid1.id) // prefer younger
	}
	return false
}
