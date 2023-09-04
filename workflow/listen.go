package workflow

import (
	"github.com/lunfardo314/proxima/core"
	utxo_tangle "github.com/lunfardo314/proxima/utangle"
)

type Events struct {
	pi *Workflow
}

func (w *Workflow) UTXOTangle() *utxo_tangle.UTXOTangle {
	return w.utxoTangle
}

func (w *Workflow) Events() *Events {
	return &Events{w}
}

func (ev Events) ListenTransactions(fun func(vid *utxo_tangle.WrappedTx)) error {
	return ev.pi.OnEvent(EventNewVertex, func(inp *NewVertexEventData) {
		fun(inp.VID)
	})
}

func (ev Events) ListenSequencers(fun func(vertex *utxo_tangle.WrappedTx)) error {
	return ev.ListenTransactions(func(v *utxo_tangle.WrappedTx) {
		if v.IsSequencerMilestone() {
			fun(v)
		}
	})
}

func (ev Events) ListenSequencer(seqID core.ChainID, fun func(vid *utxo_tangle.WrappedTx)) error {
	return ev.ListenSequencers(func(vid *utxo_tangle.WrappedTx) {
		if id, available := vid.SequencerIDIfAvailable(); available && id == seqID {
			fun(vid)
		}
	})
}

func (ev Events) ListenAccount(accountable core.Accountable, fun func(wOut utxo_tangle.WrappedOutput)) error {
	return ev.ListenTransactions(func(vid *utxo_tangle.WrappedTx) {
		vid.Unwrap(utxo_tangle.UnwrapOptions{Vertex: func(v *utxo_tangle.Vertex) {
			for i := 0; i < v.Tx.NumProducedOutputs(); i++ {
				out := v.Tx.MustProducedOutputAt(byte(i))
				if core.LockIsIndexableByAccount(out.Lock(), accountable) {
					fun(utxo_tangle.WrappedOutput{
						VID:   vid,
						Index: byte(i),
					})
				}
			}
		}})
	})
}
