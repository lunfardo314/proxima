package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/queues/gossip"
	"github.com/lunfardo314/proxima/core/queues/pull_client"
	"github.com/lunfardo314/proxima/core/queues/txinput"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/txmetadata"
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
	w.log.Infof("DropTxID %s", txid.StringShort())
	attacher.InvalidateTxID(*txid, w, fmt.Errorf(reasonFormat, args...))
}

func (w *Workflow) GossipTransaction(inp *txinput.Input) {
	var receivedFrom *peer.ID
	if inp.TxSource == txinput.TransactionSourcePeer {
		receivedFrom = &inp.ReceivedFromPeer
	}
	w.gossip.Push(&gossip.Input{
		Tx:           inp.Tx,
		ReceivedFrom: receivedFrom,
		Metadata:     inp.TxMetadata,
	})
}

func (w *Workflow) PokeMe(me, with *vertex.WrappedTx) {
	w.poker.PokeMe(me, with)
}

func (w *Workflow) PokeAllWith(wanted *vertex.WrappedTx) {
	w.poker.PokeAllWith(wanted)
}

func (w *Workflow) TxBytesStore() global.TxBytesStore {
	return w.txBytesStore
}

func (w *Workflow) QueryTransactionsFromRandomPeer(lst ...ledger.TransactionID) bool {
	return w.peers.PullTransactionsFromRandomPeer(lst...)
}

func (w *Workflow) AttachTransaction(inp *txinput.Input, opts ...attacher.Option) {
	attacher.AttachTransaction(inp.Tx, w, opts...)
}

func (w *Workflow) TransactionIn(txBytes []byte, opts ...txinput.TransactionInOption) (*transaction.Transaction, error) {
	return w.txInput.TransactionInReturnTx(txBytes, opts...)
}

func (w *Workflow) SequencerMilestoneAttachWait(txBytes []byte, timeout ...time.Duration) (*transaction.Transaction, error) {
	return w.txInput.SequencerMilestoneAttachWait(txBytes, timeout...)
}

func (w *Workflow) SendTxBytesToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool {
	return w.peers.SendTxBytesToPeer(id, txBytes, metadata)
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
