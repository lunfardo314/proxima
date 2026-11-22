package conewalk

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

// frontier is the working set used both for `C` (unprocessed) and `visited`
// (recently-processed, kept only as long as some future C entry might
// reference it). Indexed by txid for O(1) membership and by slot for fast
// max-slot lookup and bulk pruning.
type frontier struct {
	txs    map[base.TransactionID]*transaction.Transaction
	bySlot map[uint32]map[base.TransactionID]struct{}
}

func newFrontier() *frontier {
	return &frontier{
		txs:    make(map[base.TransactionID]*transaction.Transaction),
		bySlot: make(map[uint32]map[base.TransactionID]struct{}),
	}
}

func (f *frontier) Add(tx *transaction.Transaction) bool {
	id := tx.ID()
	if _, ok := f.txs[id]; ok {
		return false
	}
	f.txs[id] = tx
	s := tx.Timestamp().Slot
	bucket, ok := f.bySlot[s]
	if !ok {
		bucket = make(map[base.TransactionID]struct{})
		f.bySlot[s] = bucket
	}
	bucket[id] = struct{}{}
	return true
}

func (f *frontier) Remove(id base.TransactionID) bool {
	tx, ok := f.txs[id]
	if !ok {
		return false
	}
	delete(f.txs, id)
	s := tx.Timestamp().Slot
	delete(f.bySlot[s], id)
	if len(f.bySlot[s]) == 0 {
		delete(f.bySlot, s)
	}
	return true
}

func (f *frontier) Get(id base.TransactionID) (*transaction.Transaction, bool) {
	tx, ok := f.txs[id]
	return tx, ok
}

func (f *frontier) Has(id base.TransactionID) bool {
	_, ok := f.txs[id]
	return ok
}

func (f *frontier) Len() int { return len(f.txs) }

// MaxSlot returns the highest slot present and whether the frontier is non-empty.
// Cost is O(|distinct slots|) — bounded by DAG thickness, ≪ |txs|.
func (f *frontier) MaxSlot() (uint32, bool) {
	var maxS uint32
	found := false
	for s := range f.bySlot {
		if !found || s > maxS {
			maxS = s
			found = true
		}
	}
	return maxS, found
}

// AnyAtSlot returns an arbitrary tx at the given slot, or (nil, false) if none.
func (f *frontier) AnyAtSlot(s uint32) (*transaction.Transaction, bool) {
	bucket, ok := f.bySlot[s]
	if !ok {
		return nil, false
	}
	for id := range bucket {
		return f.txs[id], true
	}
	return nil, false
}

// DeleteWhereSlotGreaterThan drops all entries with slot > threshold. Used to
// prune `visited` once the max slot still pending in `C` has dropped past
// them — those entries can no longer be needed as input producers for any
// remaining C entry (deps go strictly older as we walk backward).
func (f *frontier) DeleteWhereSlotGreaterThan(threshold uint32) int {
	deleted := 0
	for s, bucket := range f.bySlot {
		if s <= threshold {
			continue
		}
		for id := range bucket {
			delete(f.txs, id)
			deleted++
		}
		delete(f.bySlot, s)
	}
	return deleted
}
