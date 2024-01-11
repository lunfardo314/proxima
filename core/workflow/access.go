package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txinput"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/txmetadata"
)

func (w *Workflow) MaxDurationInTheFuture() time.Duration {
	return ledger.TimeSlotDuration()
}

func (w *Workflow) IncCounter(name string) {
	w.debugCounters.Inc(name)
}

func (w *Workflow) StopPulling(txid *ledger.TransactionID) {
	w.log.Infof("StopPulling %s", txid.StringShort())
}

func (w *Workflow) DropTxID(txid *ledger.TransactionID, who string, reasonFormat string, args ...any) {
	w.log.Infof("DropTxID %s", txid.StringShort())
	attacher.InvalidateTxID(*txid, w, fmt.Errorf(reasonFormat, args...))
}

func (w *Workflow) AttachTransaction(inp *txinput.Input) {
	attacher.AttachTransaction(inp.Tx, w, attacher.OptionInvokedBy("workflow"))
}

func (w *Workflow) GossipTransaction(inp *txinput.Input) {
	w.log.Infof("GossipTransaction %s", inp.Tx.IDShortString())
}

func (w *Workflow) Pull(txid ledger.TransactionID) {
	//TODO implement me
	panic("implement me")
}

func (w *Workflow) PokeMe(me, with *vertex.WrappedTx) {
	//TODO implement me
	panic("implement me")
}

func (w *Workflow) PokeAllWith(wanted *vertex.WrappedTx) {
	//TODO implement me
	panic("implement me")
}

func (w *Workflow) TxBytesStore() global.TxBytesStore {
	return w.txBytesStore
}

func (w *Workflow) QueryTransactionsFromRandomPeer(lst ...ledger.TransactionID) {
	//TODO implement me
	panic("implement me")
}

func (w *Workflow) TransactionIn(txBytes []byte, opts ...txinput.TransactionInOption) (*transaction.Transaction, error) {
	//TODO implement me
	panic("implement me")
}

func (w *Workflow) SendTxBytesToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool {
	return w.peers.SendTxBytesToPeer(id, txBytes, metadata)
}

func (w *Workflow) GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int {
	return w.peers.GossipTxBytesToPeers(txBytes, metadata, except...)
}
