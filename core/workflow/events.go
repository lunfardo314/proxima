package workflow

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/eventtype"
)

var (
	EventNewTx       = eventtype.RegisterNew[*vertex.WrappedTx]("new tx") // event may be posted more than once for the transaction
	EventNewVertex   = eventtype.RegisterNew[*NewVertexEventData]("new vertex")
	EventTxDeleted   = eventtype.RegisterNew[base.TransactionID]("del tx")
	EventNewMiningTx = eventtype.RegisterNew[*NewMiningTxEventData]("new mining tx")
)

// NewVertexEventData is posted when a vertex becomes determined.
type NewVertexEventData struct {
	*transaction.Transaction
	SeqName string
}

// NewMiningTxEventData is posted for every fair-launch mine-chain transit the
// node accepts, at arrival time — after signature validation and persistence,
// but BEFORE the attach gate. It deliberately does not wait for attachment:
// an access node drops unsolicited non-sequencer transactions, so a
// mining-tx event posted after the gate would never fire there.
//
// The transaction is therefore NOT constraint-validated at this point; in
// particular its proof of work is unchecked (mineLock enforces it in the
// consumed arm, which only runs when a sequencer walks the tx into its past
// cone). Consumers must verify the mine-chain rules themselves from TxBytes.
type NewMiningTxEventData struct {
	TxID    base.TransactionID
	TxBytes []byte
}

func (w *Workflow) PostEventNewTransaction(vid *vertex.WrappedTx) {
	w.events.PostEvent(EventNewTx, vid)
}

func (w *Workflow) PostEventNewVertex(tx *transaction.Transaction, seqName string) {
	w.events.PostEvent(EventNewVertex, &NewVertexEventData{
		Transaction: tx,
		SeqName:     seqName,
	})
}

func (w *Workflow) PostEventTxDeleted(txid base.TransactionID) {
	w.events.PostEvent(EventTxDeleted, txid)
}

func (w *Workflow) PostEventNewMiningTx(txid base.TransactionID, txBytes []byte) {
	w.events.PostEvent(EventNewMiningTx, &NewMiningTxEventData{
		TxID:    txid,
		TxBytes: txBytes,
	})
}
