package workflow

import (
	"github.com/lunfardo314/proxima/core"
	utangle "github.com/lunfardo314/proxima/utangle_old"
)

type Events struct {
	pi *Workflow
}

func (w *Workflow) UTXOTangle() *utangle.UTXOTangle {
	return w.utxoTangle
}

func (w *Workflow) Events() *Events {
	return &Events{w}
}

func (ev Events) ListenTransactions(fun func(vid *utangle.WrappedTx)) error {
	return ev.pi.OnEvent(EventNewVertex, func(inp *NewVertexEventData) {
		fun(inp.VID)
	})
}

func (ev Events) ListenSequencers(fun func(vertex *utangle.WrappedTx)) error {
	return ev.ListenTransactions(func(v *utangle.WrappedTx) {
		if v.IsSequencerMilestone() {
			fun(v)
		}
	})
}

func (ev Events) ListenSequencer(seqID core.ChainID, fun func(vid *utangle.WrappedTx)) error {
	return ev.ListenSequencers(func(vid *utangle.WrappedTx) {
		if id, available := vid.SequencerIDIfAvailable(); available && id == seqID {
			fun(vid)
		}
	})
}

func (ev Events) ListenAccount(accountable core.Accountable, fun func(wOut utangle.WrappedOutput)) error {
	counterString := "listen-" + accountable.String()
	return ev.ListenTransactions(func(vid *utangle.WrappedTx) {
		if v, ok := vid.UnwrapVertexForReadOnly(); ok {
			for i := 0; i < v.Tx.NumProducedOutputs(); i++ {
				out := v.Tx.MustProducedOutputAt(byte(i))
				if core.LockIsIndexableByAccount(out.Lock(), accountable) {
					ev.pi.debugCounters.Inc(counterString)
					fun(utangle.WrappedOutput{
						VID:   vid,
						Index: byte(i),
					})
				}
			}
		}
	})
}
