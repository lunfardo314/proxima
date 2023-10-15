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
	utxoStateDelta map[*WrappedTx]consumed

	consumed struct {
		set        set.Set[byte]
		inTheState bool
	}

	UTXOStateDelta struct {
		utxoStateDelta
		// baseline state root.
		// - if root != nil, delta is root-bound or state-bound, i.e. it is linked to a particular state.
		//   It can only be applied to that state, and it is guaranteed that it will always succeed
		// - if root == nil delta is not dependent on a particular baseline state and can be applied to any (with or without success)
		branchTxID *core.TransactionID
	}
)

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

func (d utxoStateDelta) isAlreadyIncluded(vid *WrappedTx, baselineState ...general.StateReader) bool {
	if _, alreadyIncluded := d[vid]; alreadyIncluded {
		return true
	}
	if len(baselineState) == 0 {
		return false
	}

	return baselineState[0].KnowsCommittedTransaction(vid.ID())
}

func (d utxoStateDelta) include(vid *WrappedTx, baselineState ...general.StateReader) (conflict WrappedOutput) {
	if d.isAlreadyIncluded(vid, baselineState...) {
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

func (d utxoStateDelta) getMutations(targetSlot core.TimeSlot) *multistate.Mutations {
	ret := multistate.NewMutations()

	for vid, consumedSet := range d {
		// do not touch virtual transactions
		vid.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				// DEL mutations: deleting from the baseline state all inputs which are marked consumed
				v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
					isConsumed, inTheState := d.isConsumed(WrappedOutput{VID: inp, Index: i})
					if isConsumed && inTheState {
						ret.InsertDelOutputMutation(v.Tx.MustInputAt(i))
					}
					return true
				})
				if consumedSet.inTheState {
					// do not produce anything if transaction is already in the state
					return
				}
				// SET mutations: adding outputs of not-in-the-state state transaction which are not
				// marked as consumed. If all outputs are consumed, adding nothing
				v.Tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
					if !consumedSet.set.Contains(idx) {
						ret.InsertAddOutputMutation(*oid, o)
					}
					return true
				})
				// ADDTX mutation: adding records for all new transactions (not in the state already).
				// Even of those which have no produced outputs, because all of them have been consumed in the delta
				ret.InsertAddTxMutation(*v.Tx.ID(), targetSlot)
			},
			Orphaned: PanicOrphaned,
		})
	}
	return ret.Sort()
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

func NewUTXOState(branchTxID *core.TransactionID) *UTXOStateDelta {
	return &UTXOStateDelta{
		utxoStateDelta: make(utxoStateDelta),
		branchTxID:     branchTxID,
	}
}

func (d *UTXOStateDelta) Include(vid *WrappedTx, getBaselineState func(branchTxID *core.TransactionID) general.StateReader) (ret WrappedOutput) {
	if d.branchTxID != nil {
		return d.utxoStateDelta.include(vid, getBaselineState(d.branchTxID))
	}
	return d.utxoStateDelta.include(vid)
}

func MergeVertexDeltas(getStateReader func(branchTxID *core.TransactionID) general.StateReader, vids ...*WrappedTx) (*UTXOStateDelta, *WrappedOutput) {
	deltas := make([]*UTXOStateDelta, len(vids))
	for i, vid := range vids {
		deltas[i] = vid.GetUTXOStateDelta()
	}
	return MergeDeltas(getStateReader, deltas...)
}

func MergeDeltas(getStateReader func(branchTxID *core.TransactionID) general.StateReader, deltas ...*UTXOStateDelta) (*UTXOStateDelta, *WrappedOutput) {
	ret := make(utxoStateDelta)
	if len(deltas) == 0 {
		return &UTXOStateDelta{utxoStateDelta: ret}, nil
	}

	// find baseline state, the latest not-nil state
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
	var baselineStateArg []general.StateReader
	latestBranchTxID := deltasSorted[0].branchTxID
	if latestBranchTxID != nil {
		baselineStateArg = util.List(getStateReader(latestBranchTxID))
	}

	var conflict WrappedOutput
	for i, d := range deltasSorted {
		if i == 0 {
			ret = deltasSorted[0].utxoStateDelta.clone()
			continue
		}
		if conflict = ret.append(d.utxoStateDelta, baselineStateArg...); conflict.VID != nil {
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
