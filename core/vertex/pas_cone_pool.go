package vertex

import (
	"sync"

	"golang.org/x/exp/maps"
)

var _pastConePool sync.Pool

func NewPastConeBase(baseline *WrappedTx) *PastConeBase {
	if r := _pastConePool.Get(); r != nil {
		ret := r.(*PastConeBase)
		ret.baseline = baseline
	}
	return newPastConeBase(baseline)
}

func DisposePastConeBase(pb *PastConeBase) {
	pb.baseline = nil
	maps.Clear(pb.vertices)
	maps.Clear(pb.virtuallyConsumed)
	_pastConePool.Put(pb)
}
