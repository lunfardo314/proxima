package workflow

import (
	"github.com/lunfardo314/proxima/core/txmetadata"
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
// For sequencer transactions, TransactionMetadata fields (coverage, supply, etc.)
// are populated from the attacher wrapup. For non-sequencer transactions those remain nil.
type NewVertexEventData struct {
	*transaction.Transaction
	txmetadata.TransactionMetadata
	SeqName          string
	ProposerStrategy string
}

func (w *Workflow) PostEventNewTransaction(vid *vertex.WrappedTx) {
	w.events.PostEvent(EventNewTx, vid)
}

func (w *Workflow) PostEventNewVertex(tx *transaction.Transaction, metadata *txmetadata.TransactionMetadata, seqName, proposerStrategy string) {
	data := &NewVertexEventData{
		Transaction:      tx,
		SeqName:          seqName,
		ProposerStrategy: proposerStrategy,
	}
	if metadata != nil {
		data.TransactionMetadata = *metadata
	}
	w.events.PostEvent(EventNewVertex, data)
}

func (w *Workflow) PostEventTxDeleted(txid base.TransactionID) {
	w.events.PostEvent(EventTxDeleted, txid)
}
