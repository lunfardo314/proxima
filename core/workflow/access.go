package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/set"
)

func (w *Workflow) MaxDurationInTheFuture() time.Duration {
	return 10 * ledger.SlotDuration()
}

func (w *Workflow) PokeMe(me, with *vertex.WrappedTx) {
	w.poker.PokeMe(me, with)
}

func (w *Workflow) PokeAllWith(wanted *vertex.WrappedTx) {
	w.poker.PokeAllWith(wanted)
}

func (w *Workflow) SendTxBytesToPeer(id peer.ID, txBytes []byte, txid base.TransactionID) bool {
	return w.peers.SendTxBytesToPeer(id, txBytes, txid)
}

func (w *Workflow) GossipTxBytesToPeers(txBytes []byte, txid base.TransactionID, except ...peer.ID) {
	w.peers.GossipTxBytesToPeers(txBytes, txid, except...)
}

func (w *Workflow) MustPersistTxBytes(tx *transaction.Transaction) {
	w.txStoreWriter.PersistTxBytesQueued(tx)
}

// GetTxBytes checks the transaction cache first, then the underlying store.
func (w *Workflow) GetTxBytes(txid *base.TransactionID) []byte {
	if data := w.txStoreWriter.GetTxBytes(txid); data != nil {
		return data
	}
	return w.TxBytesStore().GetTxBytes(txid)
}

// TakeCachedTx returns a pre-parsed transaction from the cache and removes it.
// Returns nil if not cached. The write buffer is not affected.
func (w *Workflow) TakeCachedTx(txid *base.TransactionID) *transaction.Transaction {
	return w.txStoreWriter.TakeCachedTx(txid)
}

func (w *Workflow) SendToTippool(vid *vertex.WrappedTx) {
	w.tippool.Push(tippool.Input{WrappedTx: vid})
}

func (w *Workflow) IsSynced() bool {
	slotNow := ledger.TimeNow().Slot
	return slotNow == 0 || multistate.FirstHealthySlotIsNotBefore(w.StateStore(), slotNow-1)
}

func (w *Workflow) MaxConcurrentAttachers() int {
	return w.cfg.maxConcurrentAttachers
}

func (w *Workflow) NotifyBranchCommitted(branchSlot uint32) {
	w.syncModule.NotifyBranchCommitted(branchSlot)
}

// RecordBranchSlotFromPeers bumps the high-water mark of branch slots heard from
// peers (monotonic max). Called by txInputQueue for validated peer branch txs.
func (w *Workflow) RecordBranchSlotFromPeers(slot uint32) {
	for {
		cur := w.latestBranchSlotFromPeers.Load()
		if slot <= cur {
			return
		}
		if w.latestBranchSlotFromPeers.CompareAndSwap(cur, slot) {
			return
		}
	}
}

// LatestBranchSlotFromPeers returns the highest branch slot heard from peers, the
// forward-sync anchor. 0 if none heard yet.
func (w *Workflow) LatestBranchSlotFromPeers() uint32 {
	return w.latestBranchSlotFromPeers.Load()
}

// RequestPrune signals the memDAG to run LRB-depth pruning on the next tick.
func (w *Workflow) RequestPrune() {
	w.MemDAG.RequestPrune()
}

// RegisterBranchVertices records the vertex set of a branch's past cone for fine-grained pruning.
func (w *Workflow) RegisterBranchVertices(branchID base.TransactionID, predecessorBranchID base.TransactionID, vertices set.Set[*vertex.WrappedTx]) {
	w.MemDAG.RegisterBranchVertices(branchID, predecessorBranchID, vertices)
}

func (w *Workflow) ForceCommitBranch(branchID base.TransactionID) {
	w.branches.GetStateReaderForTheBranch(branchID)
}

// AttachmentDepthCap returns the recursive-pull depth cap (in branches), fixed at
// startup from configuration. Read opaquely by attachers — see sync_semantics.md §2.
func (w *Workflow) AttachmentDepthCap() int {
	return w.attachmentDepthCap
}

// VertexTTLSlots returns the memDAG wall-clock vertex TTL (in slots), so a milestone attacher can
// self-abort before its own vertex is force-detached by the size backstop.
func (w *Workflow) VertexTTLSlots() uint32 {
	return memdag.VertexTTLSlots()
}

// ForwardSyncEnabled reports whether forward sync is active (i.e. 'sources' are configured).
// When false, recursion is the only catch-up mechanism, so an attacher that hits the depth cap
// shuts the node down instead of registering a sync target no module can service.
func (w *Workflow) ForwardSyncEnabled() bool {
	return w.syncModule != nil
}

// IsSyncing returns true when forward-sync is actively catching up.
func (w *Workflow) IsSyncing() bool {
	return w.syncModule.IsSyncing()
}

// OnCanonicalLineage reports whether the node's committed LRB is on the network's canonical lineage.
// Delegates to the sync module (nil when forward sync is disabled → true: no determination, do not
// block the sequencer gate). See claude/archive/incidents/fork_detection_recovery.md §3.
func (w *Workflow) OnCanonicalLineage() bool {
	return w.syncModule.OnCanonicalLineage()
}

// IsVertexReferencedInTippool returns true if the vertex is one of the latest milestone tips.
func (w *Workflow) IsVertexReferencedInTippool(vid *vertex.WrappedTx) bool {
	return w.tippool.IsVertexReferenced(vid)
}

// LatestMilestonesDescending returns optionally filtered sorted transactions from the sequencer tippool
func (w *Workflow) LatestMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	return w.tippool.LatestActiveMilestonesDescending(filter...)
}

// LatestMilestonesShuffled returns optionally filtered sorted transactions from the sequencer tippool
func (w *Workflow) LatestMilestonesShuffled(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	return w.tippool.LatestActiveMilestonesShuffled(filter...)
}

func (w *Workflow) GetLatestMilestone(seqID base.ChainID) *vertex.WrappedTx {
	return w.tippool.GetLatestActiveMilestone(seqID)
}

func (w *Workflow) NumSequencerTips() int {
	return w.tippool.NumSequencerTips()
}

func (w *Workflow) PeerName(id peer.ID) string {
	return w.peers.PeerName(id)
}

func (w *Workflow) QueryTxIDStatus(txid base.TransactionID) (ret vertex.TxIDStatus) {
	ret = w.MemDAG.QueryTxIDStatus(txid)
	ret.InStorage = w.TxBytesStore().HasTxBytes(&txid)
	return
}

func (w *Workflow) WaitTxIDDefined(txid base.TransactionID, pollPeriod, timeout time.Duration) (vertex.Status, error) {
	deadline := time.Now().Add(timeout)
	for {
		status := w.QueryTxIDStatus(txid)
		if status.Status != vertex.Undefined {
			return status.Status, nil
		}
		time.Sleep(pollPeriod)
		if time.Now().After(deadline) {
			return vertex.Undefined, fmt.Errorf("timeout")
		}
	}
}

func (w *Workflow) AddPulledTransaction(txid base.TransactionID) {
	w.txInputQueue.AddPulledTransaction(txid)
}

// CachedTxInSolicited sends a pre-parsed transaction from cache to the solicit queue for fast-track attachment.
func (w *Workflow) CachedTxInSolicited(tx *transaction.Transaction) {
	w.txSolicitQueue.PushParsedTx(tx)
}

// TxBytesFromStoreInSolicited sends raw txstore bytes to the solicit queue (fallback for disk-only lookups).
func (w *Workflow) TxBytesFromStoreInSolicited(txBytesWithMetadata []byte) {
	w.txSolicitQueue.PushTxBytesFromStore(txBytesWithMetadata)
}

// PipelineSize returns the total number of transactions in the processing pipeline:
// memDAG vertices + solicited queue length + txstore cache + txs waiting for clock alignment.
func (w *Workflow) PipelineSize() int {
	return w.NumVertices() + w.txSolicitQueue.Len() + w.txStoreWriter.CacheSize() + w.Counter("wait")
}

func (w *Workflow) GetKnownLatestSequencerDataJSONAble() map[string]tippool.LatestSequencerTipDataJSONAble {
	return w.tippool.GetKnownLatestSequencerDataJSONAble()
}

func (w *Workflow) DisableMemDAGGC() bool {
	return w.cfg.disableMemDAGGC
}

func (w *Workflow) Branches() *branches.Branches {
	return w.branches
}

// CheckTransactionInLRB shadows MemDAG.CheckTransactionInLRB to use Branches.FindLatestReliableBranch
// (which sees pending branches) and Branches.BranchKnowsTransaction (which walks pending mutations).
func (w *Workflow) CheckTransactionInLRB(txid base.TransactionID, maxDepth int) (lrbid base.TransactionID, foundAtDepth int) {
	foundAtDepth = -1
	lrb := w.branches.FindLatestReliableBranch()
	if lrb == nil {
		return
	}
	lrbid = lrb.Stem.ID.TransactionID()

	multistate.IterateBranchChainBack(w.StateStore(), lrb, func(branchID *base.TransactionID, branch *multistate.BranchData) bool {
		if foundAtDepth >= maxDepth {
			return false
		}
		if !w.branches.BranchKnowsTransaction(*branchID, txid) {
			return false
		}
		foundAtDepth++
		return true
	})
	return
}
