package vertex

import (
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	ReferencedSet struct {
		Committed set.Set[*WrappedTx]
		Delta     set.Set[*WrappedTx]
	}

	PastCone struct {
		Vertices   map[*WrappedTx]byte // byte is usd by attacher for flags
		Rooted     map[*WrappedTx]set.Set[byte]
		Referenced ReferencedSet
	}

	PastConeSnapshot struct {
		PastCone
		Coverage uint64
	}
)

func NewPastCone() *PastCone {
	return &PastCone{
		Vertices:   make(map[*WrappedTx]byte),
		Rooted:     make(map[*WrappedTx]set.Set[byte]),
		Referenced: NewReferencedSet(),
	}
}

func NewReferencedSet() ReferencedSet {
	return ReferencedSet{Committed: set.New[*WrappedTx]()}
}

func (r *ReferencedSet) BeginDelta() {
	util.Assertf(r.Delta == nil, "r.Delta == nil")
	r.Delta = set.New[*WrappedTx]()
}

func (r *ReferencedSet) CommitDelta() {
	r.Delta.ForEach(func(vid *WrappedTx) bool {
		r.Committed.Insert(vid)
		return true
	})
	r.Delta = nil
}

func (r *ReferencedSet) RollbackDelta() {
	r.Delta.ForEach(func(vid *WrappedTx) bool {
		vid.UnReference()
		return true
	})
	r.Delta = nil
}

// Reference references transaction and ensures it is referenced once or none
func (r *ReferencedSet) Reference(vid *WrappedTx) bool {
	if r.Committed.Contains(vid) {
		return true
	}
	if r.Delta != nil && r.Delta.Contains(vid) {
		return true
	}
	if !vid.Reference() {
		// failed to reference
		return false
	}
	if r.Delta != nil {
		// Delta buffer is open
		r.Delta.Insert(vid)
	} else {
		r.Committed.Insert(vid)
	}
	return true
}

func (r *ReferencedSet) MustReference(vid *WrappedTx) {
	util.Assertf(r.Reference(vid), "r.reference(vid)")
}

func (r *ReferencedSet) UnReferenceAll() {
	r.RollbackDelta()
	r.Committed.ForEach(func(vid *WrappedTx) bool {
		vid.UnReference()
		return true
	})
	r.Committed = set.New[*WrappedTx]()
}
