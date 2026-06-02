package workflow

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/eventtype"
)

var (
	EventNewTx     = eventtype.RegisterNew[*vertex.WrappedTx]("new tx") // event may be posted more than once for the transaction
	EventNewVertex = eventtype.RegisterNew[*NewVertexEventData]("new vertex")
	EventTxDeleted = eventtype.RegisterNew[base.TransactionID]("del tx")
)

// NewVertexEventData is posted when a vertex becomes determined.
type NewVertexEventData struct {
	*transaction.Transaction
	SeqName string
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
