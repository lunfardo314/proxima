package attacher

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/set"
)

// solidifyPastCone solidifies and validates sequencer transaction in the context of known baseline state
func (a *sequencerAttacher) solidifyPastCone() vertex.Status {
	return a.lazyRepeat(func() (status vertex.Status) {
		ok := true
		success := false
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				ok = a.attachVertex(v, a.vid, ledger.NilLogicalTime, set.New[*vertex.WrappedTx]())
				if ok {
					success = v.FlagsUp(vertex.FlagsSequencerVertexCompleted)
				}
			},
		})
		switch {
		case !ok:
			return vertex.Bad
		case success:
			return vertex.Good
		default:
			return vertex.Undefined
		}
	})
}
