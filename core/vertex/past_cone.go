package vertex

import (
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	FlagsPastCone byte

	PastCone struct {
		*pastConeBase
		delta *pastConeBase
	}

	pastConeBase struct {
		baseline *WrappedTx
		Vertices map[*WrappedTx]FlagsPastCone // byte is used by attacher for flags
		Rooted   map[*WrappedTx]set.Set[byte]
	}
)

func newPastConeBase(baseline *WrappedTx) *pastConeBase {
	return &pastConeBase{
		Vertices: make(map[*WrappedTx]FlagsPastCone),
		Rooted:   make(map[*WrappedTx]set.Set[byte]),
		baseline: baseline,
	}
}

func NewPastCone() *PastCone {
	return &PastCone{pastConeBase: newPastConeBase(nil)}
}

func (pb *pastConeBase) referenceBaseline(vid *WrappedTx) bool {
	util.Assertf(pb.baseline == nil, "pc.baseline == nil")
	if !vid.Reference() {
		return false
	}
	pb.baseline = vid
	return true
}

func (pc *PastCone) _base() *pastConeBase {
	if pc.delta != nil {
		return pc.delta
	}
	return pc.pastConeBase
}

func (pc *PastCone) ReferenceBaseline(vid *WrappedTx) bool {
	pb := pc.pastConeBase
	if pc.delta != nil {
		pb = pc.delta
	}
	return pb.referenceBaseline(vid)
}

func (pc *PastCone) BeginDelta() {
	util.Assertf(pc.delta == nil, "pc.delta == nil")
	pc.delta = newPastConeBase(pc.baseline)
}

func (pc *PastCone) CommitDelta() {
	util.Assertf(pc.delta != nil, "pc.delta != nil")
	util.Assertf(pc.baseline == nil || pc.baseline == pc.delta.baseline, "pc.baseline==nil || pc.baseline == pc.delta.baseline")
	pc.baseline = pc.delta.baseline

	for vid, flags := range pc.delta.Vertices {
		pc.Vertices[vid] = flags
	}
	var consumed set.Set[byte]
	for vid, newConsumed := range pc.delta.Rooted {
		if consumed = pc.Rooted[vid]; consumed == nil {
			consumed = set.New[byte]()
			pc.Rooted[vid] = consumed
		}
		consumed.AddAll(newConsumed)
	}
	pc.delta = nil
}

func (pc *PastCone) RollbackDelta() {
	util.Assertf(pc.delta != nil, "pc.delta != nil")
	for vid := range pc.delta.Vertices {
		vid.UnReference()
	}
	if pc.delta.baseline != nil && pc.baseline == nil {
		pc.delta.baseline.UnReference()
	}
	pc.delta = nil
}

func (pc *PastCone) UnReferenceAll() {
	pc.RollbackDelta()
	if pc.baseline != nil {
		pc.baseline.UnReference()
	}
	for vid := range pc.Vertices {
		vid.UnReference()
	}
}

//// Reference references transaction and ensures it is referenced once or none
//func (pc *PastCone) Reference(vid *WrappedTx) bool {
//	if pc.referenced.committed.Contains(vid) {
//		return true
//	}
//	if pc.referenced.delta != nil && pc.referenced.delta.Contains(vid) {
//		return true
//	}
//	if !vid.Reference() {
//		// failed to reference
//		return false
//	}
//	if pc.referenced.delta != nil {
//		// Delta buffer is open
//		pc.referenced.delta.Insert(vid)
//	} else {
//		pc.referenced.committed.Insert(vid)
//	}
//	return true
//}
//
//func (pc *PastCone) MustReference(vid *WrappedTx) {
//	util.Assertf(pc.Reference(vid), "pc.Reference(vid)")
//}
