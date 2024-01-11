package workflow

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
)

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
