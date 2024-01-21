package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/queues/gossip"
	"github.com/lunfardo314/proxima/core/queues/persist_txbytes"
	"github.com/lunfardo314/proxima/core/queues/pull_client"
	"github.com/lunfardo314/proxima/core/queues/txinput"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func (w *Workflow) MaxDurationInTheFuture() time.Duration {
	return ledger.SlotDuration()
}

func (w *Workflow) IncCounter(name string) {
	w.debugCounters.Inc(name)
}

func (w *Workflow) Pull(txid ledger.TransactionID) {
	w.pullClient.Push(&pull_client.Input{
		TxIDs: []ledger.TransactionID{txid},
	})
}

func (w *Workflow) StopPulling(txid *ledger.TransactionID) {
	w.pullClient.StopPulling(txid)
}

func (w *Workflow) DropTxID(txid *ledger.TransactionID, who string, reasonFormat string, args ...any) {
	w.Tracef("workflow", "DropTxID %s", txid.StringShort())
	attacher.InvalidateTxID(*txid, w, fmt.Errorf(reasonFormat, args...))
}

func (w *Workflow) GossipTransaction(inp *txinput.Input) {
	util.Assertf(!inp.TxMetadata.IsResponseToPull, "!inp.TxMetadata.IsResponseToPull")
	var receivedFrom *peer.ID
	if inp.TxMetadata.SourceTypeNonPersistent == txmetadata.SourceTypePeer {
		receivedFrom = &inp.ReceivedFromPeer
	}
	metadata := inp.TxMetadata
	metadata.SourceTypeNonPersistent = txmetadata.SourceTypeForward

	w.gossip.Push(&gossip.Input{
		Tx:           inp.Tx,
		ReceivedFrom: receivedFrom,
		Metadata:     metadata,
	})
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

func (w *Workflow) AttachTransaction(inp *txinput.Input, opts ...attacher.Option) {
	attacher.AttachTransaction(inp.Tx, w, opts...)
}

func (w *Workflow) TxBytesIn(txBytes []byte, opts ...txinput.TransactionInOption) (*ledger.TransactionID, error) {
	return w.txInput.TxBytesIn(txBytes, opts...)
}

func (w *Workflow) SequencerMilestoneAttachWait(txBytes []byte, timeout time.Duration) (*vertex.WrappedTx, error) {
	return w.txInput.SequencerMilestoneNewAttachWait(txBytes, timeout)
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
	w.persistTxBytes.Push(persist_txbytes.Input{
		TxBytes:  txBytes,
		Metadata: metadata,
	})
}
