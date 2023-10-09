package utangle

import (
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

type (
	UTXOStateDelta2 struct {
		// baseline state root.
		// - if root != nil, delta is root-bound or state-bound, i.e. it is linked to a particular state.
		//   It can only bne applied to that state, and it is guaranteed that it will always succeed
		// - if root == nil delta is not dependent on a particular baseline state and can be applied to any (with or without success)
		root         common.VCommitment
		transactions map[*WrappedTx]set.Set[byte]
	}
)

func NewUTXOStateDelta2(root ...common.VCommitment) *UTXOStateDelta2 {
	ret := &UTXOStateDelta2{
		transactions: make(map[*WrappedTx]set.Set[byte]),
	}
	if len(root) > 0 {
		ret.root = root[0]
	}
	return ret
}

func (d *UTXOStateDelta2) isStateBound() bool {
	return d.root == common.VCommitment(nil)
}

func (d *UTXOStateDelta2) getConsumedSet(vid *WrappedTx) set.Set[byte] {
	return d.transactions[vid]
}

// TODO optimize with WrappedOutput iterator, better GC-wise

func (d *UTXOStateDelta2) consume(getStateReader func(root common.VCommitment) general.StateReader, wOuts ...WrappedOutput) WrappedOutput {
	util.Assertf(!d.isStateBound() || getStateReader != nil, "state reader closure must be provided for state-bound delta")

	var rdr general.StateReader
	var stateReaderLoaded bool

	for _, wOut := range wOuts {
		consumedSet := d.getConsumedSet(wOut.VID)
		if consumedSet.Contains(wOut.Index) {
			return wOut
		}
		// not found in the consumed set
		if d.isStateBound() {
			// check in the state
			if !stateReaderLoaded {
				rdr = getStateReader(d.root)
				stateReaderLoaded = true
			}
			if !rdr.HasUTXO(wOut.DecodeID()) {
				return wOut
			}
		}
		if len(consumedSet) == 0 {
			consumedSet = set.New[byte](wOut.Index)
		} else {
			consumedSet.Insert(wOut.Index)
		}
		d.transactions[wOut.VID] = consumedSet
	}
	return WrappedOutput{}
}

func (d *UTXOStateDelta2) isIncluded(vid *WrappedTx) bool {
	_, included := d.transactions[vid]
	return included
}

func (d *UTXOStateDelta2) include(getStateReader func(root common.VCommitment) general.StateReader, vid *WrappedTx) (conflict WrappedOutput) {
	if !d.isIncluded(vid) {
		d.transactions[vid] = nil
		conflict = d.consume(getStateReader, vid.WrappedInputs()...)
	}
	return
}

func mergeDeltas(getStateReader func(root common.VCommitment) general.StateReader, deltas ...*UTXOStateDelta2) (ret *UTXOStateDelta2, conflict WrappedOutput) {
	if len(deltas) == 0 {
		return
	}

	stateBound := make([]*UTXOStateDelta2, 0)
	notStateBound := make([]*UTXOStateDelta2, 0)
	for _, d := range deltas {
		if d.isStateBound() {
			stateBound = append(stateBound, d)
		} else {
			notStateBound = append(notStateBound, d)
		}
	}
	ret, conflict = mergeStateIndependentDeltas(notStateBound...)
	if conflict.VID != nil {
		return
	}
	switch {
	case len(stateBound) == 0:
		return
	case len(stateBound) == 1:
		// not GC-efficient
		ret.root = stateBound[0].root
		wrappedOutputs := make([]WrappedOutput, 0)
		for vid, consumedSet := range stateBound[0].transactions {
			for idx := range consumedSet {
				wrappedOutputs = append(wrappedOutputs, WrappedOutput{
					VID:   vid,
					Index: idx,
				})
			}
		}
		conflict = ret.consume(getStateReader, wrappedOutputs...)
		if conflict.VID != nil {
			ret = nil
			return
		}
	default:
		panic("not implemented")
	}
	return
}

func mergeStateIndependentDeltas(deltas ...*UTXOStateDelta2) (ret *UTXOStateDelta2, conflict WrappedOutput) {
	ret = NewUTXOStateDelta2()
	for _, d := range deltas {
		for vid, consumedSet := range d.transactions {
			consumedSet.ForEach(func(i byte) bool {
				conflict = ret.consume(nil, WrappedOutput{
					VID:   vid,
					Index: i,
				})
				return conflict.VID != nil
			})
			if conflict.VID != nil {
				ret = nil
				return
			}
		}
	}
	return
}
