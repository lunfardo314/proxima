// Package branches: virtual_state_reader.go
//
// virtualStateReader implements multistate.StateReader by overlaying pending branch
// mutations on top of a committed ancestor state. This avoids forcing DB commits
// of pending branches when their state is accessed read-only (e.g., during sequencer
// proposal evaluation).
//
// Background:
// Branch commits are lazy/deferred: AddPendingBranch stores mutations with Root = nil,
// and the actual DB write only happens when GetStateReaderForTheBranch() is called.
// On a sequencer node, the proposer strategies create IncrementalAttachers for many
// endorsement candidates in parallel. Each attacher needs to read the baseline branch
// state for conflict checking and output verification. Without a virtual reader, this
// forces ALL branches to be committed to DB — even though most will be discarded.
//
// Design:
// The virtual reader holds a chain of mutation layers (newest to oldest) and a
// committed ancestor IndexedStateReader. For each query (HasUTXO, GetUTXO,
// KnowsCommittedTransaction), it checks mutations from newest to oldest:
//   - If the item was added in a mutation layer → found
//   - If the item was deleted in a mutation layer → not found
//   - If not referenced in any layer → delegate to committed ancestor
//
// This correctly models the state after applying all pending mutations without
// writing anything to DB. The ancestor is always a committed branch whose state
// reader is already cached.
//
// Limitations:
// The virtual reader only implements StateReader (GetUTXO, HasUTXO,
// KnowsCommittedTransaction). It does NOT implement StateIndexReader methods like
// Root(), IterateUTXOs(), GetUTXOForChainID(), etc. This is sufficient because the
// hot path (past cone conflict checking, coverage delta, output verification) only
// uses StateReader methods.
//
// Thread safety:
// Mutations are immutable once created (built during attachment, never modified after
// being added to the pending map). The ancestor reader has its own internal mutex.
// The virtual reader itself is safe for concurrent reads.

package branches

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

// virtualStateReader overlays pending branch mutations on a committed ancestor.
// It implements multistate.StateReader without requiring a DB commit.
type virtualStateReader struct {
	// layers contains mutations from newest (current branch) to oldest
	// (nearest committed ancestor's direct child). Each layer represents
	// one pending branch's mutations relative to its predecessor.
	layers []*multistate.Mutations

	// ancestor is the committed branch state at the end of the pending chain.
	// All queries that aren't resolved by mutation layers fall through to this.
	ancestor multistate.StateReader
}

// GetUTXO returns the serialized output bytes for the given output ID.
// It checks pending mutations from newest to oldest before falling through
// to the committed ancestor state.
func (v *virtualStateReader) GetUTXO(id base.OutputID) ([]byte, bool) {
	for _, mut := range v.layers {
		// Check if this branch added the output
		if o, found := mut.FindAddedOutput(id); found {
			return o.Bytes(), true
		}
		// Check if this branch consumed (deleted) the output
		if mut.HasDeletedOutput(id) {
			return nil, false
		}
	}
	// Not modified in any pending branch — delegate to committed state
	return v.ancestor.GetUTXO(id)
}

// HasUTXO checks whether an output exists in the virtual state.
// Same layered lookup as GetUTXO but returns only existence.
func (v *virtualStateReader) HasUTXO(id base.OutputID) bool {
	for _, mut := range v.layers {
		if _, found := mut.FindAddedOutput(id); found {
			return true
		}
		if mut.HasDeletedOutput(id) {
			return false
		}
	}
	return v.ancestor.HasUTXO(id)
}

// KnowsCommittedTransaction checks whether a transaction ID is known in the virtual state.
// Transaction IDs are added to the state when their branch is committed and removed
// after TTL expiry. The virtual reader checks pending AddTx/DelTx mutations.
func (v *virtualStateReader) KnowsCommittedTransaction(txid base.TransactionID) bool {
	for _, mut := range v.layers {
		if mut.HasTx(txid) {
			return true
		}
		if mut.HasDeletedTx(txid) {
			return false
		}
	}
	return v.ancestor.KnowsCommittedTransaction(txid)
}

// buildVirtualStateReader constructs a virtual state reader for a pending branch.
// It walks back through the pending branch chain via stem links (PreviousBranchID),
// collecting mutation layers, until it reaches a committed branch.
//
// The caller must hold b.mutex.
func (b *Branches) buildVirtualStateReader(branchID base.TransactionID) multistate.StateReader {
	var layers []*multistate.Mutations

	currentID := branchID
	for {
		pb, isPending := b.pending[currentID]
		if !isPending {
			// Reached a committed branch — get its state reader as the ancestor.
			// We must unlock before calling GetStateReaderForTheBranch (it takes its own lock).
			b.mutex.Unlock()
			ancestor := b.GetStateReaderForTheBranch(currentID)
			b.mutex.Lock()
			if ancestor == nil {
				// Branch not found (shouldn't happen in normal operation)
				return nil
			}
			return &virtualStateReader{
				layers:   layers,
				ancestor: ancestor,
			}
		}
		// Collect this branch's mutations and walk to predecessor
		layers = append(layers, pb.Mutations)
		currentID = pb.PreviousBranchID
	}
}

// GetVirtualStateReaderForTheBranch returns a StateReader for the branch without
// forcing a DB commit. If the branch is already committed, it returns the committed
// reader directly. If the branch is pending, it builds a virtual reader that overlays
// mutations on the nearest committed ancestor.
//
// Use this instead of GetStateReaderForTheBranch when you only need StateReader
// methods (GetUTXO, HasUTXO, KnowsCommittedTransaction) and want to avoid
// triggering unnecessary branch commits. The primary use case is the IncrementalAttacher
// during sequencer proposal evaluation.
func (b *Branches) GetVirtualStateReaderForTheBranch(branchID base.TransactionID) multistate.StateReader {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// If the branch is not pending, return the committed reader directly
	if _, isPending := b.pending[branchID]; !isPending {
		// unlock/relock around GetStateReaderForTheBranch since it takes its own lock
		b.mutex.Unlock()
		rdr := b.GetStateReaderForTheBranch(branchID)
		b.mutex.Lock()
		return rdr
	}

	return b.buildVirtualStateReader(branchID)
}
