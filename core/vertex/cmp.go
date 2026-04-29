package vertex

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// IsPreferredMilestoneAgainstTheOther returns if vid1 is strongly better than vid2
// 'better' means aligned coverage is bigger, or, if equal, transaction id is smaller
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
		return base.LessTxID(vid2.id, vid1.id) // prefer younger
	}
	return false
}
