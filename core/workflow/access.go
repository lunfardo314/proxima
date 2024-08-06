package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/gossip"
	"github.com/lunfardo314/proxima/core/work_process/persist_txbytes"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

// TODO revisit MaxDurationInTheFuture

func (w *Workflow) MaxDurationInTheFuture() time.Duration {
	return ledger.SlotDuration() * 5
}

func (w *Workflow) Pull(txid ledger.TransactionID) {
	w.pullClient.Pull(&txid)
}

func (w *Workflow) StopPulling(txid *ledger.TransactionID) {
	w.pullClient.StopPulling(txid)
}

func (w *Workflow) GossipAttachedTransaction(tx *transaction.Transaction, metadata *txmetadata.TransactionMetadata) {
	w.GossipTransactionIfNeeded(tx, metadata, nil)
}

func (w *Workflow) GossipTransactionIfNeeded(tx *transaction.Transaction, metadata *txmetadata.TransactionMetadata, receivedFromPeer *peer.ID) {
	w.Assertf(metadata != nil, "metadata!=nil")
	if metadata.DoNotNeedGossiping {
		return
	}
	metadata.DoNotNeedGossiping = true
	if metadata.IsResponseToPull {
		return
	}
	w.gossip.Push(&gossip.Input{
		Tx:           tx,
		ReceivedFrom: receivedFromPeer,
		Metadata:     *metadata,
	})
}

func (w *Workflow) PokeMe(me, with *vertex.WrappedTx) {
	w.poker.PokeMe(me, with)
}

func (w *Workflow) PokeAllWith(wanted *vertex.WrappedTx) {
	w.poker.PokeAllWith(wanted)
}

func (w *Workflow) NotifyEndOfPortion() {
	if w.syncManager != nil {
		w.syncManager.NotifyEndOfPortion()
	}
}

func (w *Workflow) PullTransactionsFromRandomPeer(lst ...ledger.TransactionID) bool {
	return w.peers.PullTransactionsFromRandomPeer(lst...)
}

func (w *Workflow) PullTransactionsFromAllPeers(lst ...ledger.TransactionID) {
	w.peers.PullTransactionsFromAllPeers(lst...)
}

func (w *Workflow) SendTxBytesWithMetadataToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool {
	return w.peers.SendTxBytesWithMetadataToPeer(id, txBytes, metadata)
}

func (w *Workflow) GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int {
	return w.peers.GossipTxBytesToPeers(txBytes, metadata, except...)
}

func (w *Workflow) AsyncPersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata) {
	w.Tracef(persist_txbytes.TraceTag, "AsyncPersistTxBytesWithMetadata: %d bytes, meta: %s", len(txBytes), metadata.String())
	w.persistTxBytes.Push(persist_txbytes.Input{
		TxBytes:  txBytes,
		Metadata: metadata,
	})
}

func (w *Workflow) TxBytesWithMetadataIn(txBytes []byte, metadata *txmetadata.TransactionMetadata) (*ledger.TransactionID, error) {
	return w.TxBytesIn(txBytes, WithMetadata(metadata))
}

func (w *Workflow) SendToTippool(vid *vertex.WrappedTx) {
	w.tippool.Push(tippool.Input{VID: vid})
}

func (w *Workflow) IsSynced() bool {
	slotNow := ledger.TimeNow().Slot()
	util.Assertf(slotNow > 0, "slotNow > 0")
	return multistate.FirstHealthySlotIsNotBefore(w.StateStore(), slotNow-1, global.FractionHealthyBranch)
}

// LatestMilestonesDescending returns optionally filtered sorted transactions from the sequencer tippool
func (w *Workflow) LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	return w.tippool.LatestMilestonesDescending(filter...)
}

func (w *Workflow) GetLatestMilestone(seqID ledger.ChainID) *vertex.WrappedTx {
	return w.tippool.GetLatestMilestone(seqID)
}

func (w *Workflow) NumSequencerTips() int {
	return w.tippool.NumSequencerTips()
}

func (w *Workflow) PeerName(id peer.ID) string {
	return w.peers.PeerName(id)
}

func (w *Workflow) QueryTxIDStatus(txid *ledger.TransactionID) (ret vertex.TxIDStatus) {
	ret = w.MemDAG.QueryTxIDStatus(txid)
	ret.InStorage = w.TxBytesStore().HasTxBytes(txid)
	return
}

func (w *Workflow) QueryTxIDStatusJSONAble(txid *ledger.TransactionID) vertex.TxIDStatusJSONAble {
	ret := w.QueryTxIDStatus(txid)
	return ret.JSONAble()
}

func (w *Workflow) GetTxInclusion(txid *ledger.TransactionID, slotsBack int) *multistate.TxInclusion {
	return multistate.GetTxInclusion(w.StateStore(), txid, slotsBack)
}

func (w *Workflow) WaitTxIDDefined(txid *ledger.TransactionID, pollPeriod, timeout time.Duration) (vertex.Status, error) {
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

func (w *Workflow) PullSyncPortion(startingFrom ledger.Slot, maxSlots int, servers ...string) {
	w.peers.PullSyncPortionFromRandomPeer(startingFrom, maxSlots, servers...)
}
