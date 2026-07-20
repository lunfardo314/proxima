package workflow

import (
	"bytes"
	"sync"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// ListenToControllerAccount listens to all outputs that are
// unlockable by the controller, except stem-locked outputs
// - ordinary sigLock-ed UTXOs
// - ordinary chainLocked-ed UTXOs
// - delegation output has 2 controller, so delegation output will be seen either by
// target, or master listener. It is up to the callback to filter UTXO that are exactly needed
func (w *Workflow) ListenToControllerAccount(controller ledger.Controller, fun func(wOut vertex.WrappedOutput)) {
	w.events.OnEvent(EventNewTx, func(vid *vertex.WrappedTx) {
		var _indices [256]byte
		indices := _indices[:0]
		seqData := vid.SequencerTransactionData()
		vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			v.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
				// skip stem outputs; otherwise match outputs whose
				// index-value tuple contains the controller's bytes.
				if seqData != nil && idx == seqData.StemOutputIndex {
					return true
				}
				cid := controller.ControllerID()
				for _, v := range o.IndexValues() {
					if bytes.Equal(v, cid) {
						indices = append(indices, idx)
						return true
					}
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
	mutex                  sync.Mutex
	deleteHandlerCounter   int
	deleteHandlers         map[int]func(txid base.TransactionID) bool
	vertexHandlerCounter   int
	vertexHandlers         map[int]func(data *NewVertexEventData) bool
	miningTxHandlerCounter int
	miningTxHandlers       map[int]func(data *NewMiningTxEventData) bool
}

func (w *Workflow) startListeningTransactions() {
	w.txListener = &txListener{
		deleteHandlers:   make(map[int]func(txid base.TransactionID) bool),
		vertexHandlers:   make(map[int]func(data *NewVertexEventData) bool),
		miningTxHandlers: make(map[int]func(data *NewMiningTxEventData) bool),
	}
	w.events.OnEvent(EventNewVertex, func(data *NewVertexEventData) {
		w.txListener.runForVertex(data)
	})
	w.events.OnEvent(EventTxDeleted, func(txid base.TransactionID) {
		w.txListener.runForDelete(txid)
	})
	w.events.OnEvent(EventNewMiningTx, func(data *NewMiningTxEventData) {
		w.txListener.runForMiningTx(data)
	})
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

func (tl *txListener) runForVertex(data *NewVertexEventData) {
	tl.mutex.Lock()
	defer tl.mutex.Unlock()

	for id, fun := range tl.vertexHandlers {
		if !fun(data) {
			delete(tl.vertexHandlers, id)
		}
	}
}

// runForMiningTx dispatches to the registered mining-tx handlers. Event
// dispatch is single-threaded for the whole node, so a handler that blocks here
// stalls every other event consumer: handlers must not do network I/O.
func (tl *txListener) runForMiningTx(data *NewMiningTxEventData) {
	tl.mutex.Lock()
	defer tl.mutex.Unlock()

	for id, fun := range tl.miningTxHandlers {
		if !fun(data) {
			delete(tl.miningTxHandlers, id)
		}
	}
}

// OnNewMiningTx registers a handler for fair-launch mine-chain transits. The
// handler is removed once it returns false.
func (w *Workflow) OnNewMiningTx(fun func(data *NewMiningTxEventData) bool) {
	w.txListener.mutex.Lock()
	defer w.txListener.mutex.Unlock()

	w.txListener.miningTxHandlers[w.txListener.miningTxHandlerCounter] = fun
	w.txListener.miningTxHandlerCounter++
}

func (w *Workflow) OnNewVertex(fun func(data *NewVertexEventData) bool) {
	w.txListener.mutex.Lock()
	defer w.txListener.mutex.Unlock()

	w.txListener.vertexHandlers[w.txListener.vertexHandlerCounter] = fun
	w.txListener.vertexHandlerCounter++
}

func (w *Workflow) OnTxDeleted(fun func(txid base.TransactionID) bool) {
	w.txListener.mutex.Lock()
	defer w.txListener.mutex.Unlock()

	w.txListener.deleteHandlers[w.txListener.deleteHandlerCounter] = fun
	w.txListener.deleteHandlerCounter++
}
