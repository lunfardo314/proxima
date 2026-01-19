package workflow

import (
	"sync"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

// ListenToAccount listens to all outputs that belong to the account (except stem-locked outputs)
func (w *Workflow) ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput)) {
	w.events.OnEvent(EventNewTx, func(vid *vertex.WrappedTx) {
		var _indices [256]byte
		indices := _indices[:0]
		vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			v.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
				if ledger.BelongsToAccount(o.Lock(), account) && o.Lock().Name() != ledger.StemLockName {
					indices = append(indices, idx)
				}
				return true
			})
		}})
		for _, idx := range indices {
			fun(vertex.WrappedOutput{
				VID:   vid,
				Index: idx,
			})
		}
	})
}

type txListener struct {
	mutex                sync.Mutex
	handlerCounter       int
	handlers             map[int]func(tx *transaction.Transaction) bool
	deleteHandlerCounter int
	deleteHandlers       map[int]func(txid base.TransactionID) bool
}

func (w *Workflow) startListeningTransactions() {
	w.txListener = &txListener{
		handlers:       make(map[int]func(tx *transaction.Transaction) bool),
		deleteHandlers: make(map[int]func(txid base.TransactionID) bool),
	}
	w.events.OnEvent(EventNewTx, func(vid *vertex.WrappedTx) {
		var tx *transaction.Transaction

		vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			tx = v.Transaction
		}})
		if tx != nil {
			// no need for goroutine because events are on queue
			w.txListener.runFor(tx)
		}
	})
	w.events.OnEvent(EventTxDeleted, func(txid base.TransactionID) {
		w.txListener.runForDelete(txid)
	})
}

func (tl *txListener) runFor(tx *transaction.Transaction) {
	tl.mutex.Lock()
	defer tl.mutex.Unlock()

	for id, fun := range tl.handlers {
		if !fun(tx) {
			delete(tl.handlers, id)
		}
	}
}

func (tl *txListener) runForDelete(txid base.TransactionID) {
	tl.mutex.Lock()
	defer tl.mutex.Unlock()

	for id, fun := range tl.deleteHandlers {
		if !fun(txid) {
			delete(tl.deleteHandlers, id)
		}
	}
}

func (w *Workflow) OnTransaction(fun func(tx *transaction.Transaction) bool) {
	w.txListener.mutex.Lock()
	defer w.txListener.mutex.Unlock()

	w.txListener.handlers[w.txListener.handlerCounter] = fun
	w.txListener.handlerCounter++
}

func (w *Workflow) OnTxDeleted(fun func(txid base.TransactionID) bool) {
	w.txListener.mutex.Lock()
	defer w.txListener.mutex.Unlock()

	w.txListener.deleteHandlers[w.txListener.deleteHandlerCounter] = fun
	w.txListener.deleteHandlerCounter++
}
