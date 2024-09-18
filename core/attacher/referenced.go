package attacher

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

// referencedSet represents a buffer for referencing transactions with the rollback possibility if beginDelta is not called
type referencedSet struct {
	committed set.Set[*vertex.WrappedTx]
	delta     set.Set[*vertex.WrappedTx]
}

func newReferencedSet() referencedSet {
	return referencedSet{committed: set.New[*vertex.WrappedTx]()}
}

func (r *referencedSet) beginDelta() {
	util.Assertf(r.delta == nil, "r.delta == nil")
	r.delta = set.New[*vertex.WrappedTx]()
}

func (r *referencedSet) commitDelta() {
	r.delta.ForEach(func(vid *vertex.WrappedTx) bool {
		r.committed.Insert(vid)
		return true
	})
	r.delta = nil
}

func (r *referencedSet) rollbackDelta() {
	r.delta.ForEach(func(vid *vertex.WrappedTx) bool {
		vid.UnReference()
		return true
	})
	r.delta = nil
}

// reference references transaction and ensures it is referenced once or none
func (r *referencedSet) reference(vid *vertex.WrappedTx) bool {
	if r.committed.Contains(vid) {
		return true
	}
	if r.delta != nil && r.delta.Contains(vid) {
		return true
	}
	if !vid.Reference() {
		// failed to reference
		return false
	}
	if r.delta != nil {
		// delta buffer is open
		r.delta.Insert(vid)
	} else {
		r.committed.Insert(vid)
	}
	return true
}

func (r *referencedSet) mustReference(vid *vertex.WrappedTx) {
	util.Assertf(r.reference(vid), "r.reference(vid)")
}

func (r *referencedSet) unReferenceAll() {
	r.rollbackDelta()
	r.committed.ForEach(func(vid *vertex.WrappedTx) bool {
		vid.UnReference()
		return true
	})
	r.committed = set.New[*vertex.WrappedTx]()
}
