package workflow

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/txstore"
)

// branchListMaxSlots bounds the slot range of one PullBranchList request, so one request costs
// a node at most this many slot scans of its txstore.
const branchListMaxSlots = 150

// serveDAGStores answers the lineage requests of the node's trusted dagstores, from the txstore
// alone: the lineage a dagstore needs reaches back as far as the txstore does, the ledger state
// does not.
func (w *Workflow) serveDAGStores() {
	if !w.peers.HasTrustedDAGStores() {
		return
	}
	w.peers.OnPullLRB(w.answerPullLRB)
	w.peers.OnPullBranchList(w.answerPullBranchList)
}

func (w *Workflow) answerPullLRB(from peer.ID) {
	lrb := w.GetLatestReliableBranch()
	if lrb == nil {
		return
	}
	r := peering.LRBReply{LRB: lrb.Stem.ID.TransactionID()}
	if src, ok := w.TxBytesStore().(txstore.Iterable); ok {
		r.OldestSlot, _ = txstore.OldestSlot(src)
	}
	w.peers.SendLRBReply(from, r)
}

func (w *Workflow) answerPullBranchList(from peer.ID, fromSlot, toSlot uint32) {
	src, ok := w.TxBytesStore().(txstore.Iterable)
	if !ok || toSlot < fromSlot || toSlot-fromSlot >= branchListMaxSlots {
		return
	}
	entries := make([]peering.BranchListEntry, 0)
	for slot := fromSlot; ; slot++ {
		for _, branchID := range txstore.BranchesInSlot(src, slot) {
			tx, err := transaction.Parse(w.TxBytesStore().GetTxBytes(&branchID))
			if err != nil {
				continue
			}
			if pred, ok := tx.StemPredecessor(); ok {
				entries = append(entries, peering.BranchListEntry{Branch: branchID, Predecessor: pred})
			}
		}
		if slot == toSlot {
			break
		}
	}
	w.peers.SendBranchListReply(from, fromSlot, toSlot, entries)
}
