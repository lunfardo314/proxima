// Package conewalk walks the past cone of a set of transactions over raw transaction bytes alone:
// no ledger state, no memDAG. It serves `proxi db txstore audit` and the dagstore.
package conewalk

import (
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

// Walker walks backward from its roots, always taking a transaction of the newest slot still
// pending, so transactions are visited in non-increasing slot order. A dependency (input producer,
// endorsement, explicit baseline) is never newer than its referrer, which is what lets the walker
// forget visited transactions once the pending frontier has dropped below their slot.
type Walker struct {
	src   global.TxBytesGet
	parse func([]byte) (*transaction.Transaction, error)
	floor uint32

	pending     *frontier
	visited     *frontier
	branchesInC map[uint32]int // slot -> branches pending at that slot

	// Skip, if set, is asked about every dependency before it is loaded; true leaves it out of the
	// walk (neither loaded nor reported missing), e.g. because the caller already holds its cone.
	Skip func(dep, referrer base.TransactionID) bool
	// Visit is called for each visited transaction, after its dependencies are discovered.
	Visit func(tx *transaction.Transaction)
	// Missing is called for each reference to a dependency at or above the floor that the source
	// does not hold, or cannot parse.
	Missing func(dep, referrer base.TransactionID)
	// BelowFloor is called for each reference to a dependency older than the floor, which the
	// walk does not follow.
	BelowFloor func(dep, referrer base.TransactionID)
	// BranchSlotComplete is called when the last pending branch of a slot has been visited.
	BranchSlotComplete func(slot uint32)

	ParseErrors int
}

// New makes a walker over src that follows dependencies down to slot floor. parse turns bytes into
// a transaction: transaction.ParseLibraryAgnostic suffices for the structure, transaction.Parse is
// needed to read outputs.
func New(src global.TxBytesGet, floor uint32, parse func([]byte) (*transaction.Transaction, error)) *Walker {
	return &Walker{
		src:         src,
		parse:       parse,
		floor:       floor,
		pending:     newFrontier(),
		visited:     newFrontier(),
		branchesInC: make(map[uint32]int),
	}
}

// Add loads txid and adds it to the walk as a root. It returns false if the source does not hold
// it or cannot parse it.
func (w *Walker) Add(txid base.TransactionID) bool {
	tx, ok := w.load(txid)
	if ok {
		w.addPending(tx)
	}
	return ok
}

// Run walks until nothing is pending at or above the floor.
func (w *Walker) Run() {
	for {
		maxSlot, ok := w.pending.MaxSlot()
		if !ok || maxSlot < w.floor {
			return
		}
		tx, _ := w.pending.AnyAtSlot(maxSlot)
		w.visitOne(tx)
	}
}

// Get returns a transaction that is pending or was recently visited. Visited transactions are kept
// only while something pending may still reference them.
func (w *Walker) Get(txid base.TransactionID) (*transaction.Transaction, bool) {
	if tx, ok := w.pending.Get(txid); ok {
		return tx, true
	}
	return w.visited.Get(txid)
}

// Sizes returns the number of pending and of retained visited transactions.
func (w *Walker) Sizes() (pending, visited int) {
	return w.pending.Len(), w.visited.Len()
}

// MaxPendingSlot returns the newest slot still pending.
func (w *Walker) MaxPendingSlot() (uint32, bool) {
	return w.pending.MaxSlot()
}

func (w *Walker) visitOne(tx *transaction.Transaction) {
	id := tx.ID()
	w.discoverDeps(tx)
	w.pending.Remove(id)
	w.visited.Add(tx)
	if w.Visit != nil {
		w.Visit(tx)
	}
	if !tx.IsBranchTransaction() {
		return
	}
	slot := tx.Slot()
	if w.branchesInC[slot]--; w.branchesInC[slot] > 0 {
		return
	}
	delete(w.branchesInC, slot)
	// nothing newer than the newest pending slot can be referenced again
	if maxSlot, ok := w.pending.MaxSlot(); ok {
		w.visited.DeleteWhereSlotGreaterThan(maxSlot)
	} else {
		w.visited = newFrontier()
	}
	if w.BranchSlotComplete != nil {
		w.BranchSlotComplete(slot)
	}
}

func (w *Walker) discoverDeps(tx *transaction.Transaction) {
	id := tx.ID()
	follow := func(dep base.TransactionID) {
		if dep.Slot() < w.floor {
			if w.BelowFloor != nil {
				w.BelowFloor(dep, id)
			}
			return
		}
		if w.pending.Has(dep) || w.visited.Has(dep) {
			return
		}
		if w.Skip != nil && w.Skip(dep, id) {
			return
		}
		depTx, ok := w.load(dep)
		if !ok {
			if w.Missing != nil {
				w.Missing(dep, id)
			}
			return
		}
		w.addPending(depTx)
	}
	tx.ForEachInputID(func(_ byte, oid base.OutputID) bool {
		follow(oid.TransactionID())
		return true
	})
	tx.ForEachEndorsement(func(_ byte, dep base.TransactionID) bool {
		follow(dep)
		return true
	})
	if baseline, ok := tx.ExplicitBaseline(); ok {
		follow(baseline)
	}
}

func (w *Walker) load(txid base.TransactionID) (*transaction.Transaction, bool) {
	txBytes := w.src.GetTxBytes(&txid)
	if len(txBytes) == 0 {
		return nil, false
	}
	tx, err := w.parse(txBytes)
	if err != nil {
		w.ParseErrors++
		return nil, false
	}
	return tx, true
}

func (w *Walker) addPending(tx *transaction.Transaction) {
	if w.pending.Add(tx) && tx.IsBranchTransaction() {
		w.branchesInC[tx.Slot()]++
	}
}
