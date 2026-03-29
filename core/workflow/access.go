package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
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

func (w *Workflow) SendTxBytesWithMetadataToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID) bool {
	return w.peers.SendTxBytesWithMetadataToPeer(id, txBytes, metadata, txid)
}

func (w *Workflow) GossipAttachedTransaction(tx *transaction.Transaction, metadata *txmetadata.TransactionMetadata) {
	if metadata != nil {
		if metadata.SourceTypeNonPersistent == txmetadata.SourceTypeTxStore || metadata.SourceTypeNonPersistent == txmetadata.SourceTypePulled {
			return
		}
	}
	w.GossipTxBytesToPeers(tx.Bytes(), metadata, tx.ID())
}

func (w *Workflow) GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID, except ...peer.ID) {
	w.peers.GossipTxBytesToPeers(txBytes, metadata, txid, except...)
}

func (w *Workflow) MustPersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata, txid ...base.TransactionID) {
	if len(txid) > 0 {
		w.txStoreWriter.PersistTxBytesQueued(txBytes, metadata, txid[0])
	} else {
		// fallback: synchronous write (no txid provided, rare path)
		_, err := w.TxBytesStore().PersistTxBytesWithMetadata(txBytes, metadata)
		util.AssertNoError(err)
	}
}

// GetTxBytesWithMetadata checks the write-behind buffer first, then the underlying store.
func (w *Workflow) GetTxBytesWithMetadata(txid *base.TransactionID) []byte {
	if data := w.txStoreWriter.GetPending(txid); data != nil {
		return data
	}
	return w.TxBytesStore().GetTxBytesWithMetadata(txid)
}

func (w *Workflow) SendToTippool(vid *vertex.WrappedTx) {
	w.tippool.Push(tippool.Input{WrappedTx: vid})
}

func (w *Workflow) IsSynced() bool {
	slotNow := ledger.TimeNow().Slot
	return slotNow == 0 || multistate.FirstHealthySlotIsNotBefore(w.StateStore(), slotNow-1, global.FractionHealthyBranch)
}

func (w *Workflow) MaxConcurrentAttachers() int {
	return w.cfg.maxConcurrentAttachers
}

func (w *Workflow) NotifyBranchCommitted(branchSlot uint32) {
	w.syncModule.NotifyBranchCommitted(branchSlot)
}

func (w *Workflow) ForceCommitBranch(branchID base.TransactionID) {
	w.branches.GetStateReaderForTheBranch(branchID)
}

func (w *Workflow) LatestForwardSyncedTimestamp() base.LedgerTime {
	return w.syncModule.LatestForwardSyncedTimestamp()
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

// TxBytesFromStoreInSolicited sends txstore bytes to the solicit queue for fast-track attachment.
func (w *Workflow) TxBytesFromStoreInSolicited(txBytesWithMetadata []byte) {
	w.txSolicitQueue.PushTxBytesFromStore(txBytesWithMetadata)
}

func (w *Workflow) SaveFullDAG(fname string) {
	branchTxIDS := multistate.FetchLatestBranchTransactionIDs(w.StateStore())
	tmpDag := memdag.MakeDAGFromTxStoreUntilSlot(w.TxBytesStore(), 0, branchTxIDS...)
	tmpDag.SaveGraph(fname)
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
