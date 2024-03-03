package workflow

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/gossip"
	"github.com/lunfardo314/proxima/core/work_process/persist_txbytes"
	"github.com/lunfardo314/proxima/core/work_process/pull_client"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

// TODO revisit MaxDurationInTheFuture

func (w *Workflow) MaxDurationInTheFuture() time.Duration {
	return ledger.SlotDuration() * 5
}

func (w *Workflow) Pull(txid ledger.TransactionID) {
	w.pullClient.Push(&pull_client.Input{
		TxIDs: []ledger.TransactionID{txid},
	})
}

func (w *Workflow) StopPulling(txid *ledger.TransactionID) {
	w.pullClient.StopPulling(txid)
}

func (w *Workflow) GossipTransaction(tx *transaction.Transaction, metadata *txmetadata.TransactionMetadata, receivedFromPeer *peer.ID) {
	inp := &gossip.Input{
		Tx:           tx,
		ReceivedFrom: receivedFromPeer,
	}
	if metadata != nil {
		util.Assertf(!metadata.IsResponseToPull, "!metadata.IsResponseToPull")
		inp.Metadata = *metadata
	}
	w.gossip.Push(inp)
}

func (w *Workflow) PokeMe(me, with *vertex.WrappedTx) {
	w.poker.PokeMe(me, with)
}

func (w *Workflow) PokeAllWith(wanted *vertex.WrappedTx) {
	w.poker.PokeAllWith(wanted)
}

func (w *Workflow) QueryTransactionsFromRandomPeer(lst ...ledger.TransactionID) bool {
	return w.peers.PullTransactionsFromRandomPeer(lst...)
}

func (w *Workflow) SendTxBytesWithMetadataToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool {
	return w.peers.SendTxBytesWithMetadataToPeer(id, txBytes, metadata)
}

func (w *Workflow) GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int {
	return w.peers.GossipTxBytesToPeers(txBytes, metadata, except...)
}

func (w *Workflow) EvidenceIncomingBranch(txid *ledger.TransactionID, seqID ledger.ChainID) {
	w.syncData.EvidenceIncomingBranch(txid, seqID)
}

func (w *Workflow) EvidenceBookedBranch(txid *ledger.TransactionID, seqID ledger.ChainID) {
	w.syncData.EvidenceBookedBranch(txid, seqID)
}

func (w *Workflow) SyncData() *SyncData {
	return w.syncData
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

func (w *Workflow) LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	return w.tippool.LatestMilestonesDescending(filter...)
}

func (w *Workflow) GetLatestMilestone(seqID ledger.ChainID) *vertex.WrappedTx {
	return w.tippool.GetLatestMilestone(seqID)
}

func (w *Workflow) NumSequencerTips() int {
	return w.tippool.NumSequencerTips()
}
