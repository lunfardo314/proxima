package multistate

import (
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
)

// AbsoluteCoverage is the absolute coverage of a branch at its own slot (kb/dag_semantics.md, D8):
// the predecessor's absolute coverage halved once per slot between them, plus the coverage delta,
// halved once per slot it skips. The delta covers the predecessor's state, so a branch on a deep
// baseline starts near zero however large its delta. For a normal branch, one slot after its
// predecessor, it is the stored ledger coverage. Expects predSlot < slot.
func AbsoluteCoverage(predAbsolute uint64, predSlot, slot uint32, delta uint64) uint64 {
	gap := uint64(slot - predSlot)
	return predAbsolute>>gap + delta>>(gap-1)
}

// CoverageAtSlot is the absolute coverage of a branch of slot measured at slot atSlot >= slot: halved
// once per slot of distance. Coverages are compared only at one common slot (D8).
func CoverageAtSlot(absolute uint64, slot, atSlot uint32) uint64 {
	if atSlot <= slot {
		return absolute
	}
	return absolute >> uint64(atSlot-slot)
}

// PredecessorBranchID is the branch whose stem this branch consumes, its baseline. False for the
// genesis branch.
func (bd *BranchData) PredecessorBranchID() (base.TransactionID, bool) {
	stemLock, ok := bd.Stem.Output.StemLock()
	if !ok {
		return base.TransactionID{}, false
	}
	predID := stemLock.PredecessorOutputID.TransactionID()
	if predID.Slot() >= bd.Stem.ID.Slot() {
		return base.TransactionID{}, false
	}
	return predID, true
}

// AbsoluteCoverageFromStore is the absolute coverage of the branch, taking the stored ledger coverage
// of its predecessor for the predecessor's absolute coverage. It differs from the full chain value
// only when the predecessor itself skipped slots. A branch without a stored predecessor keeps its own
// stored ledger coverage.
func AbsoluteCoverageFromStore(store global.StoreReader, bd *BranchData) uint64 {
	predID, ok := bd.PredecessorBranchID()
	if !ok {
		return bd.TotalCoverage
	}
	root, found := FetchRootRecord(store, predID)
	if !found {
		return bd.TotalCoverage
	}
	pred := FetchBranchDataByRoot(store, root)
	return AbsoluteCoverage(pred.TotalCoverage, predID.Slot(), bd.Stem.ID.Slot(), bd.CoverageDelta)
}
