package memdag

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// badBranchTTLSlots is how long a rejected branch is remembered after it was last rejected or
// referenced. A diverged node keeps building on its branch for as long as it runs, so the record
// must outlive the memDAG TTLs by far; when it expires, the branch is validated once more and
// remembered again.
const badBranchTTLSlots = 1000

type badBranchRecord struct {
	err      error
	lastSlot uint32
}

// RememberBadBranch records a branch rejected by a verdict that depends only on the branch
// transaction and its own baseline, so that a later reference to it resolves to BAD at once.
// Without it the verdict is lost when the memDAG prunes the vertex, and every new milestone
// built on the branch makes the node pull the branch and the history under it back and
// validate it again.
func (d *MemDAG) RememberBadBranch(txid base.TransactionID, err error) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.badBranches[txid] = badBranchRecord{err: err, lastSlot: ledger.TimeNow().Slot}
}

// KnownBadBranchNoLock returns the error of a remembered rejected branch, nil when the branch
// is not remembered. A hit renews the record. The caller holds the global write lock.
func (d *MemDAG) KnownBadBranchNoLock(txid base.TransactionID) error {
	rec, found := d.badBranches[txid]
	if !found {
		return nil
	}
	rec.lastSlot = ledger.TimeNow().Slot
	d.badBranches[txid] = rec
	return rec.err
}

func (d *MemDAG) pruneBadBranchesNoLock(slotNow uint32) {
	for txid, rec := range d.badBranches {
		if slotNow > rec.lastSlot+badBranchTTLSlots {
			delete(d.badBranches, txid)
		}
	}
}
