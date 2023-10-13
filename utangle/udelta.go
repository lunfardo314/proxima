package utangle

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	utxoStateDelta map[*WrappedTx]consumed

	consumed struct {
		set        set.Set[byte]
		inTheState bool
	}
)

func NewUTXOStateDelta() utxoStateDelta {
	return make(utxoStateDelta)
}

func (d utxoStateDelta) clone() utxoStateDelta {
	ret := make(utxoStateDelta)
	for vid, consumedSet := range d {
		ret[vid] = consumed{
			set:        consumedSet.set.Clone(),
			inTheState: consumedSet.inTheState,
		}
	}
	return ret
}

func (d utxoStateDelta) consume(wOut WrappedOutput, baselineState ...general.StateReader) WrappedOutput {
	consumedSet, found := d[wOut.VID]
	if found {
		// corresponding tx is already in the delta
		if consumedSet.set.Contains(wOut.Index) {
			return wOut
		}
		if consumedSet.inTheState {
			util.Assertf(len(baselineState) > 0, "baseline state not provided")
			if !baselineState[0].HasUTXO(wOut.DecodeID()) {
				return wOut
			}
		}
		if len(consumedSet.set) == 0 {
			consumedSet.set = set.New[byte](wOut.Index)
		} else {
			consumedSet.set.Insert(wOut.Index)
		}
		return WrappedOutput{}
	}
	// there's no corresponding tx in the delta
	if len(baselineState) > 0 {
		if baselineState[0].HasUTXO(wOut.DecodeID()) {
			d[wOut.VID] = consumed{
				set:        set.New[byte](wOut.Index),
				inTheState: true,
			}
			return WrappedOutput{}
		}
	}
	// no baseline state or output is not in the baseline state
	if conflict := d.include(wOut.VID, baselineState...); conflict.VID != nil {
		return conflict
	}
	consumedSet, found = d[wOut.VID]
	util.Assertf(found && !consumedSet.inTheState, "found && !consumedSet.inTheState")

	consumedSet.set = set.New[byte](wOut.Index)
	d[wOut.VID] = consumedSet
	return WrappedOutput{}
}

func (d utxoStateDelta) include(vid *WrappedTx, baselineState ...general.StateReader) (conflict WrappedOutput) {
	if _, alreadyIncluded := d[vid]; alreadyIncluded {
		return
	}
	for _, wOut := range vid.WrappedInputs() {
		// virtual tx has 0 WrappedInputs
		if conflict = d.consume(wOut, baselineState...); conflict.VID != nil {
			return
		}
	}
	d[vid] = consumed{}
	return
}

// append baselineState must be the baseline state of d. d must be consistent with the baselineState
func (d utxoStateDelta) append(delta utxoStateDelta, baselineState ...general.StateReader) (conflict WrappedOutput) {
	for vid := range delta {
		if conflict = d.include(vid, baselineState...); conflict.VID != nil {
			return
		}
	}
	return
}

func (d utxoStateDelta) coverage() (ret uint64) {
	for vid, consumedSet := range d {
		vid.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				ret += uint64(v.Tx.TotalAmount())
				consumedSet.set.ForEach(func(idx byte) bool {
					o, ok := v.MustProducedOutput(idx)
					util.Assertf(ok, "can't get output")
					ret -= o.Amount()
					return true
				})
			},
		})
	}
	return
}

func (d utxoStateDelta) isConsumed(wOut WrappedOutput) (bool, bool) {
	consumedSet, found := d[wOut.VID]
	if !found {
		return false, false
	}
	return consumedSet.set.Contains(wOut.Index), consumedSet.inTheState
}

func (d utxoStateDelta) getUpdateCommands() []multistate.UpdateCmd {
	ret := make([]multistate.UpdateCmd, 0)

	for vid, consumedSet := range d {
		vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			// SET mutations
			v.Tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
				if !consumedSet.set.Contains(idx) {
					ret = append(ret, multistate.UpdateCmd{
						ID:     oid,
						Output: o,
					})
				}
				return true
			})
			// DEL mutations
			v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
				isConsumed, inTheState := d.isConsumed(WrappedOutput{VID: inp, Index: i})
				if isConsumed && inTheState {
					oid := v.Tx.MustInputAt(i)
					ret = append(ret, multistate.UpdateCmd{
						ID: &oid,
					})
				}
				return true
			})
		}})
	}
	return ret
}

func (d utxoStateDelta) lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)

	sorted := util.SortKeys(d, func(vid1, vid2 *WrappedTx) bool {
		return vid1.Timestamp().Before(vid2.Timestamp())
	})
	for _, vid := range sorted {
		consumedSet := d[vid]
		ret.Add("%s consumed: %+v (inTheState = %v)", vid.IDShort(), util.Keys(consumedSet.set), consumedSet.inTheState)
	}
	return ret
}
