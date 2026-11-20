package vertex

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

// IsPreferredMilestoneAgainstTheOther returns if vid1 is strongly better than vid2: bigger
// ledger coverage; between two branches of equal coverage, the bigger branch inflation bonus, or
// from library upgrade 7 the larger sigma x u - B (ledger.TieBreakKey); otherwise the transaction
// ID, only to keep the order total.
//
// Competing branches of a slot tie on coverage as a rule, and every sequencer must break the
// tie the same way. The bonus is a VRF output of the issuer's key and the slot, which nobody
// can choose; a branch's transaction ID its issuer can grind by varying the branch.
func IsPreferredMilestoneAgainstTheOther(vid1, vid2 *WrappedTx) bool {
	return isPreferredMilestone(vid1, vid2, vid1.GetLedgerCoverageP(), vid2.GetLedgerCoverageP())
}

// IsPreferredMilestoneByScore is IsPreferredMilestoneAgainstTheOther on the adjusted coverage (the
// score, kb/dag_semantics.md, kb/coverage_adjustment.md), with the same tie-break. A milestone without a score loses to one with
// a score; a caller that cannot score every candidate compares by coverage instead.
func IsPreferredMilestoneByScore(vid1, vid2 *WrappedTx) bool {
	return isPreferredMilestone(vid1, vid2, vid1.GetScoreP(), vid2.GetScoreP())
}

func isPreferredMilestone(vid1, vid2 *WrappedTx, lc1, lc2 *uint64) bool {
	util.Assertf(vid1.IsSequencerTransaction() && vid2.IsSequencerTransaction(), "vid1.IsSequencerTransaction() && vid2.IsSequencerTransaction()")
	if vid1 == vid2 {
		return false
	}
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
			// from library upgrade 7 the larger sigma x u - B, a smaller bonus a better chance; before
			// it the larger bonus. The same rule for both sides keeps the order antisymmetric.
			k1, ok1 := vid1.tieBreakKey()
			k2, ok2 := vid2.tieBreakKey()
			switch {
			case ok1 && ok2:
				if k1 != k2 {
					return k1 > k2
				}
			case !ledger.L(vid1.Slot()).HasUpgrade7() && !ledger.L(vid2.Slot()).HasUpgrade7():
				if b1, b2 := vid1.InflationAmount(), vid2.InflationAmount(); b1 != b2 {
					return b1 > b2
				}
			}
		}
		return base.LessTxID(vid2.id, vid1.id) // prefer younger
	}
	return false
}

// tieBreakKey is the branch's place in the tie-break of library upgrade 7 (ledger.TieBreakKey), read
// off its stem and sequencer outputs, the same data Branches.TieBreakData gives. A branch held as a
// virtual transaction keeps both outputs, so every node orders tied branches alike whether it holds
// the transaction or only the stored branch. ok is false before the upgrade or without the outputs.
func (vid *WrappedTx) tieBreakKey() (uint64, bool) {
	lib := ledger.L(vid.Slot())
	if !lib.HasUpgrade7() {
		return 0, false
	}
	var seqOut, stemOut *ledger.Output
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			seqOut, stemOut = branchOutputs(v.Transaction)
		},
		DetachedVertex: func(v *DetachedVertex) {
			seqOut, stemOut = branchOutputs(v.Transaction)
		},
		VirtualTx: func(v *VirtualTransaction) {
			seqOut, stemOut = v.SequencerOutputs()
		},
	})
	if seqOut == nil || stemOut == nil {
		return 0, false
	}
	return ledger.TieBreakKey(lib.TieBreakSigma, ledger.BranchTieBreakDraw(stemOut), seqOut.Inflation()), true
}

func branchOutputs(tx *transaction.Transaction) (seqOut, stemOut *ledger.Output) {
	if !tx.IsBranchTransaction() {
		return nil, nil
	}
	return tx.SequencerOutput().Output, tx.StemOutput().Output
}
