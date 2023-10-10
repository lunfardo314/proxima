package utangle

import (
	"sort"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	utxoStateDelta map[*WrappedTx]set.Set[byte]

	UTXOStateDelta2 struct {
		utxoStateDelta
		// baseline state root.
		// - if root != nil, delta is root-bound or state-bound, i.e. it is linked to a particular state.
		//   It can only bne applied to that state, and it is guaranteed that it will always succeed
		// - if root == nil delta is not dependent on a particular baseline state and can be applied to any (with or without success)
		branchTxID *core.TransactionID
	}
)

func NewUTXOStateDelta2(branchTxID *core.TransactionID) *UTXOStateDelta2 {
	return &UTXOStateDelta2{
		utxoStateDelta: make(utxoStateDelta),
		branchTxID:     branchTxID,
	}
}

func (d utxoStateDelta) clone() utxoStateDelta {
	ret := make(utxoStateDelta)
	for vid, consumedSet := range d {
		if consumedSet.IsEmpty() {
			ret[vid] = nil
		} else {
			ret[vid] = consumedSet.Clone()
		}
	}
	return ret
}

func (d utxoStateDelta) consume(wOut WrappedOutput, baselineState ...general.StateReader) bool {
	consumedSet, found := d[wOut.VID]
	if found {
		return !consumedSet.Contains(wOut.Index)
	}
	// transaction is not on the delta, check the baseline state if provided
	if len(baselineState) > 0 {
		if !baselineState[0].HasUTXO(wOut.DecodeID()) {
			return false
		}
	}
	if len(consumedSet) == 0 {
		d[wOut.VID] = set.New[byte](wOut.Index)
	} else {
		d[wOut.VID].Insert(wOut.Index)
	}
	return true
}

func (d utxoStateDelta) isIncluded(vid *WrappedTx) bool {
	_, included := d[vid]
	return included
}

func (d utxoStateDelta) include(vid *WrappedTx, baselineState ...general.StateReader) (ret WrappedOutput) {
	if d.isIncluded(vid) {
		return
	}
	for _, wOut := range vid.WrappedInputs() {
		if !d.consume(wOut, baselineState...) {
			return wOut
		}
	}
	return
}

func (d *UTXOStateDelta2) Include(vid *WrappedTx, getStateReader ...func(branchTxID *core.TransactionID) general.StateReader) (ret WrappedOutput) {
	if d.branchTxID == nil {
		return d.utxoStateDelta.include(vid)
	}
	util.Assertf(len(getStateReader) > 0, "can't create state reader")
	return d.utxoStateDelta.include(vid, getStateReader[0](d.branchTxID))
}

func (d *UTXOStateDelta2) Consume(wOut WrappedOutput, getStateReader ...func(branchTxID *core.TransactionID) general.StateReader) bool {
	if d.branchTxID == nil {
		return d.utxoStateDelta.consume(wOut)
	}
	util.Assertf(len(getStateReader) > 0, "state constructor must be provided")
	return d.utxoStateDelta.consume(wOut, getStateReader[0](d.branchTxID))
}

func MergeDeltas(getStateReader func(branchTxID *core.TransactionID) general.StateReader, deltas ...*UTXOStateDelta2) (*UTXOStateDelta2, *WrappedOutput) {
	ret := make(utxoStateDelta)
	if len(deltas) == 0 {
		return &UTXOStateDelta2{utxoStateDelta: ret}, nil
	}

	deltasSorted := util.CloneArglistShallow(deltas...)
	sort.Slice(deltasSorted, func(i, j int) bool {
		bi := deltasSorted[i].branchTxID
		bj := deltasSorted[j].branchTxID
		switch {
		case bi != nil && bj == nil:
			return true
		case bi != nil && bj != nil:
			return bi.TimeSlot() > bj.TimeSlot()
		}
		return false
	})

	// check conflicting branches
	for i, d := range deltasSorted {
		if d.branchTxID == nil {
			break
		}
		if i+1 >= len(deltasSorted) {
			break
		}
		d1 := deltasSorted[i+1]
		if d1.branchTxID == nil {
			break
		}
		if d.branchTxID.TimeSlot() == d1.branchTxID.TimeSlot() && *d.branchTxID != *d1.branchTxID {
			// two different branches on the same slot conflicts
			return nil, &WrappedOutput{}
		}
	}

	// deltasSorted are all non-conflicting and sorted descending by slot with nil-branches at the end
	var baselineState general.StateReader
	latestBranchTxID := deltasSorted[0].branchTxID
	if latestBranchTxID != nil {
		baselineState = getStateReader(latestBranchTxID)
	}

	var conflict WrappedOutput
	for i, d := range deltasSorted {
		if i == 0 {
			ret = deltasSorted[0].utxoStateDelta.clone()
			continue
		}
		if conflict = ret.append(baselineState, d.utxoStateDelta); conflict.VID != nil {
			return nil, &conflict
		}
	}
	return &UTXOStateDelta2{
		utxoStateDelta: ret,
		branchTxID:     latestBranchTxID,
	}, nil
}

func (d utxoStateDelta) append(baselineState general.StateReader, delta utxoStateDelta) (conflict WrappedOutput) {
	var stateReader []general.StateReader
	if baselineState != nil {
		stateReader = util.List(baselineState)
	}
	for vid, consumeSet := range delta {
		consumeSet.ForEach(func(i byte) bool {
			wOut := WrappedOutput{
				VID:   vid,
				Index: i,
			}
			ok := d.consume(wOut, stateReader...)
			if !ok {
				conflict = wOut
			}
			return ok
		})
		if conflict.VID != nil {
			return
		}
	}
	return
}
