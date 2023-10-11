package utangle

import (
	"sort"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	utxoStateDelta map[*WrappedTx]set.Set[byte]

	UTXOStateDelta struct {
		utxoStateDelta
		// baseline state root.
		// - if root != nil, delta is root-bound or state-bound, i.e. it is linked to a particular state.
		//   It can only bne applied to that state, and it is guaranteed that it will always succeed
		// - if root == nil delta is not dependent on a particular baseline state and can be applied to any (with or without success)
		branchTxID *core.TransactionID
	}
)

func NewUTXOStateDelta(branchTxID *core.TransactionID) *UTXOStateDelta {
	return &UTXOStateDelta{
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

func (d utxoStateDelta) mustConsume(wOut WrappedOutput, mustExist bool) bool {
	consumedSet, found := d[wOut.VID]
	if mustExist {
		util.Assertf(found, "transaction %s not found on the delta", wOut.VID.IDShort())
	}

	if consumedSet.Contains(wOut.Index) {
		return false
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

func (d utxoStateDelta) include(vid *WrappedTx, baselineState ...general.StateReader) (conflict WrappedOutput) {
	if d.isIncluded(vid) {
		return
	}
	// transaction is not in the delta
	// it is expected all inputs are either in the delta or can be consumed from baseline state
	d[vid] = nil
	for _, wOut := range vid.WrappedInputs() {
		wOut.VID.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				if !d.mustConsume(wOut, true) {
					conflict = wOut
				}
			},
			VirtualTx: func(v *VirtualTransaction) {
				if len(baselineState) > 0 {
					if !baselineState[0].HasUTXO(wOut.DecodeID()) {
						conflict = wOut
						return
					}
				}
				if !d.mustConsume(wOut, false) {
					conflict = wOut
				}
			},
			Orphaned: func() {
				util.Panicf("orphaned output cannot be accessed")
			},
		})
	}
	return
}

func (d utxoStateDelta) coverage() (ret uint64) {
	for vid, consumedSet := range d {
		vid.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				ret += uint64(v.Tx.TotalAmount())
				consumedSet.ForEach(func(idx byte) bool {
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

func (d *UTXOStateDelta) Include(vid *WrappedTx, getBaselineState func(branchTxID *core.TransactionID) general.StateReader) (ret WrappedOutput) {
	if d.branchTxID != nil {
		return d.utxoStateDelta.include(vid, getBaselineState(d.branchTxID))
	}
	return d.utxoStateDelta.include(vid)
}

func MergeDeltas(getStateReader func(branchTxID *core.TransactionID) general.StateReader, deltas ...*UTXOStateDelta) (*UTXOStateDelta, *WrappedOutput) {
	ret := make(utxoStateDelta)
	if len(deltas) == 0 {
		return &UTXOStateDelta{utxoStateDelta: ret}, nil
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
	return &UTXOStateDelta{
		utxoStateDelta: ret,
		branchTxID:     latestBranchTxID,
	}, nil
}

func (d *UTXOStateDelta) Coverage(stateStore general.StateStore) uint64 {
	if d.branchTxID == nil {
		return 0
	}
	rr, found := multistate.FetchRootRecord(stateStore, *d.branchTxID)
	util.Assertf(found, "can't fetch root record")

	return rr.Coverage + d.coverage()
}

// append baselineState must be the baseline state of d. d must be consistent with the baselineState
func (d utxoStateDelta) append(baselineState general.StateReader, delta utxoStateDelta) (conflict WrappedOutput) {
	for vid, deltaConsumedSet := range delta {
		// union of 2 sets
		dConsumedSet := d[vid]
		if len(dConsumedSet) == 0 {
			dConsumedSet = set.New[byte]()
		}
		deltaConsumedSet.ForEach(func(idx byte) bool {
			dConsumedSet.Insert(idx)
			return true
		})
		// for virtual tx check if each of new consumed output can be consumed in the baseline state
		vid.Unwrap(UnwrapOptions{VirtualTx: func(v *VirtualTransaction) {
			deltaConsumedSet.ForEach(func(idx byte) bool {
				if dConsumedSet.Contains(idx) {
					// it is already consumed in the target
					return true
				}
				oid := core.NewOutputID(&v.txid, idx)
				if !baselineState.HasUTXO(&oid) {
					conflict = WrappedOutput{VID: vid, Index: idx}
					return false
				}
				return true
			})
		}})
		if conflict.VID != nil {
			return
		}
		d[vid] = dConsumedSet
	}
	return
}

func (d utxoStateDelta) isConsumedInThisDelta(wOut WrappedOutput) bool {
	consumedSet, found := d[wOut.VID]
	if !found {
		return false
	}
	return consumedSet.Contains(wOut.Index)
}

func (d utxoStateDelta) getUpdateCommands() []multistate.UpdateCmd {
	ret := make([]multistate.UpdateCmd, 0)

	for vid, consumedSet := range d {
		vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			v.Tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
				if !consumedSet.Contains(idx) {
					ret = append(ret, multistate.UpdateCmd{
						ID:     oid,
						Output: o,
					})
				}
				return true
			})
			v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
				if !d.isConsumedInThisDelta(WrappedOutput{VID: inp, Index: i}) {
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
		ret.Add("%s consumed: %+v", vid.IDShort(), util.Keys(consumedSet))
	}
	return ret
}

func (d *UTXOStateDelta) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	var baseline string
	if d.branchTxID == nil {
		baseline = "(none)"
	} else {
		baseline = d.branchTxID.Short()
	}
	ret.Add("------ START delta. Baseline: %s", baseline)
	prefix1 := ""
	if len(prefix) > 0 {
		prefix1 = prefix[0]
	}
	ret.Append(d.utxoStateDelta.lines("    " + prefix1))
	ret.Add("------ END delta")
	return ret
}
