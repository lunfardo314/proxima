package factory

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// withheldBranch tells whether the first move should pass over the branch because its chain
// predecessor was withheld (library upgrade 7, the sequencer's withholding policy): the chain
// entered the previous slot from an older output instead of its own branch of that slot; or, from
// the upgrade on, its final milestone reached this node only after the slot boundary; or that final
// milestone has a conflicting sibling stamped in the pre-branch zone. Each lets the branch bring in
// what the others had no chance to see: capital kept out of the slot, or a bid chosen after the
// branches. Where the DAG cannot tell, the branch is followed. Later in the slot such a lineage is
// followed as any other.
func withheldBranch(br *vertex.WrappedTx) (withheld bool, reason string) {
	slot := br.Slot()
	if !br.IsBranchTransaction() || slot == 0 {
		return false, ""
	}
	noReattach := func(base.TransactionID) *vertex.WrappedTx { return nil }
	final := br.SequencerPredecessor(noReattach)
	if final == nil {
		return false, ""
	}
	prevSlot := slot - 1

	// the chain must reach back to its own branch of the previous slot
	for cur := final; ; {
		if cur.Slot() < prevSlot {
			return true, "the chain entered the previous slot from an older output"
		}
		if cur.IsBranchTransaction() {
			break
		}
		pred := cur.SequencerPredecessor(noReattach)
		if pred == nil {
			break
		}
		cur = pred
	}

	if ledger.L(slot).HasUpgrade7() {
		if seen, ok := final.FirstSeen(); ok && seen.After(ledger.ClockTime(base.T(slot, 0))) {
			return true, "its final milestone arrived after the slot boundary"
		}
	}

	if final.IsBranchTransaction() {
		return false, ""
	}
	tx := final.GetTransaction()
	pred := final.SequencerPredecessor(noReattach)
	if tx == nil || pred == nil {
		return false, ""
	}
	predOID, _, err := tx.SequencerChainPredecessor()
	if err != nil {
		return false, ""
	}
	seqID := br.SequencerID.Load()
	for c := range pred.ConsumersOf(predOID.Index()) {
		if c == final || !c.IsSequencerTransaction() {
			continue
		}
		if cid := c.SequencerID.Load(); cid == nil || seqID == nil || *cid != *seqID {
			continue
		}
		if ts := c.Timestamp(); ledger.L(ts.Slot).IsPreBranchConsolidationTimestamp(ts) {
			return true, "its final milestone has a conflicting sibling in the pre-branch zone"
		}
	}
	return false, ""
}
