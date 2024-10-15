package vertex

import (
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	referencedSet struct {
		committed set.Set[*WrappedTx]
		delta     set.Set[*WrappedTx]
	}

	FlagsPastCone byte

	PastCone struct {
		Vertices   map[*WrappedTx]FlagsPastCone // byte is used by attacher for flags
		Rooted     map[*WrappedTx]set.Set[byte]
		referenced referencedSet
	}
)

func NewPastCone() *PastCone {
	return &PastCone{
		Vertices:   make(map[*WrappedTx]FlagsPastCone),
		Rooted:     make(map[*WrappedTx]set.Set[byte]),
		referenced: newReferencedSet(),
	}
}

func newReferencedSet() referencedSet {
	return referencedSet{committed: set.New[*WrappedTx]()}
}

func (pc *PastCone) BeginDelta() {
	util.Assertf(pc.referenced.delta == nil, "pc.referenced.delta == nil")
	pc.referenced.delta = set.New[*WrappedTx]()
}

func (pc *PastCone) CommitDelta() {
	pc.referenced.delta.ForEach(func(vid *WrappedTx) bool {
		pc.referenced.committed.Insert(vid)
		return true
	})
	pc.referenced.delta = nil
}

func (pc *PastCone) RollbackDelta() {
	pc.referenced.delta.ForEach(func(vid *WrappedTx) bool {
		vid.UnReference()
		return true
	})
	pc.referenced.delta = nil
}

// Reference references transaction and ensures it is referenced once or none
func (pc *PastCone) Reference(vid *WrappedTx) bool {
	if pc.referenced.committed.Contains(vid) {
		return true
	}
	if pc.referenced.delta != nil && pc.referenced.delta.Contains(vid) {
		return true
	}
	if !vid.Reference() {
		// failed to reference
		return false
	}
	if pc.referenced.delta != nil {
		// Delta buffer is open
		pc.referenced.delta.Insert(vid)
	} else {
		pc.referenced.committed.Insert(vid)
	}
	return true
}

func (pc *PastCone) MustReference(vid *WrappedTx) {
	util.Assertf(pc.Reference(vid), "pc.Reference(vid)")
}

func (pc *PastCone) UnReferenceAll() {
	pc.RollbackDelta()
	pc.referenced.committed.ForEach(func(vid *WrappedTx) bool {
		vid.UnReference()
		return true
	})
	pc.referenced.committed = set.New[*WrappedTx]()
}
