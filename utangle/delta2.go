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

func (d utxoStateDelta) getConsumedSet(vid *WrappedTx) set.Set[byte] {
	return d[vid]
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

func MergeDeltas(getStateReader func(branchTxID *core.TransactionID) general.StateReader, deltas ...*UTXOStateDelta2) (*UTXOStateDelta2, WrappedOutput) {
	ret := make(utxoStateDelta)
	if len(deltas) == 0 {
		return &UTXOStateDelta2{utxoStateDelta: ret}, WrappedOutput{}
	}

	stateBound := make([]*UTXOStateDelta2, 0)
	notStateBound := make([]utxoStateDelta, 0)
	for _, d := range deltas {
		if d.branchTxID != nil {
			stateBound = append(stateBound, d)
		} else {
			notStateBound = append(notStateBound, d.utxoStateDelta)
		}
	}
	conflict := ret.append(nil, notStateBound...)
	if conflict.VID != nil {
		return nil, conflict
	}
	if len(stateBound) == 0 {
		return &UTXOStateDelta2{utxoStateDelta: ret}, WrappedOutput{}
	}

	sort.Slice(stateBound, func(i, j int) bool {
		return stateBound[i].branchTxID.TimeSlot() < stateBound[j].branchTxID.TimeSlot()
	})

	latestDelta := stateBound[len(stateBound)-1]
	util.Assertf(getStateReader != nil, "baseline state is not provided")

	baselineState := getStateReader(latestDelta.branchTxID)
	for _, d := range stateBound {
		conflict = ret.append(baselineState, d.utxoStateDelta)
		if conflict.VID != nil {
			return nil, conflict
		}
	}
	return &UTXOStateDelta2{utxoStateDelta: ret, branchTxID: latestDelta.branchTxID}, WrappedOutput{}
}

func (d utxoStateDelta) append(baselineState general.StateReader, deltas ...utxoStateDelta) (conflict WrappedOutput) {
	var stateReader []general.StateReader
	if baselineState != nil {
		stateReader = util.List(baselineState)
	}
	for _, d1 := range deltas {
		for vid, consumeSet := range d1 {
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
	}
	return
}
