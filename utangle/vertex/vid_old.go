package vertex

import (
	"bytes"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func (vid *WrappedTx) IsDeleted() (ret bool) {
	vid.RUnwrap(UnwrapOptions{Deleted: func() {
		ret = true
	}})
	return
}

type _unwrapOptionsTraverse struct {
	UnwrapOptionsForTraverse
	visited set.Set[*WrappedTx]
}

// TraversePastConeDepthFirst performs depth-first traverse of the DAG. Visiting once each node
// and calling vertex-type specific function if provided on each.
// If function returns false, the traverse is cancelled globally.
// The traverse stops at terminal dag. The vertex is terminal if it either is not-full vertex
// i.e. (booked, orphaned, deleted) or it belongs to 'visited' set
// If 'visited' set is provided at call, it is mutable. In the end it contains all initial dag plus
// all dag visited during the traverse
func (vid *WrappedTx) TraversePastConeDepthFirst(opt UnwrapOptionsForTraverse, visited ...set.Set[*WrappedTx]) {
	var visitedSet set.Set[*WrappedTx]
	if len(visited) > 0 {
		visitedSet = visited[0]
	} else {
		visitedSet = set.New[*WrappedTx]()
	}
	vid._traversePastCone(&_unwrapOptionsTraverse{
		UnwrapOptionsForTraverse: opt,
		visited:                  visitedSet,
	})
}

func (vid *WrappedTx) _traversePastCone(opt *_unwrapOptionsTraverse) bool {
	if opt.visited.Contains(vid) {
		return true
	}
	opt.visited.Insert(vid)

	ret := true
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.ForEachInputDependency(func(i byte, inp *WrappedTx) bool {
				util.Assertf(inp != nil, "_traversePastCone: input %d is nil (not solidified) in %s",
					i, func() any { return v.Tx.IDShortString() })
				ret = inp._traversePastCone(opt)
				return ret
			})
			if ret {
				v.ForEachEndorsement(func(i byte, inpEnd *WrappedTx) bool {
					util.Assertf(inpEnd != nil, "_traversePastCone: endorsement %d is nil (not solidified) in %s",
						i, func() any { return v.Tx.IDShortString() })
					ret = inpEnd._traversePastCone(opt)
					return ret
				})
			}
			if ret && opt.Vertex != nil {
				ret = opt.Vertex(vid, v)
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			if opt.VirtualTx != nil {
				ret = opt.VirtualTx(vid, v)
			}
		},
		Deleted: func() {
			if opt.Orphaned != nil {
				ret = opt.Orphaned(vid)
			}
		},
	})
	return ret
}

func (vid *WrappedTx) InflationAmount() (ret uint64) {
	if !vid.IsBranchTransaction() {
		return
	}
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.Tx.InflationAmount()
		},
		VirtualTx: func(v *VirtualTransaction) {
			_, stemOut := v.SequencerOutputs()
			util.Assertf(stemOut != nil, "can't get stem output")
			lck, ok := stemOut.StemLock()
			util.Assertf(ok, "can't get stem output")
			ret = lck.InflationAmount
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return
}

func (vid *WrappedTx) Less(vid1 *WrappedTx) bool {
	return bytes.Compare(vid.ID()[:], vid1.ID()[:]) < 0
}
