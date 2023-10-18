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
	utxoStateDelta map[*WrappedTx]set.ByteSet

	// structure needed to prevent making target delta invalid in case of conflict during update
	// all mutations are collected in the buffer, at the (successful) end case is committed to the target delta
	// this is an optimization in order not to have to clone the whole target delta each time
	utxoStateDeltaBuffered struct {
		utxoStateDelta
		buffer utxoStateDelta
	}

	UTXOStateDelta struct {
		utxoStateDelta
		// baseline state root.
		// - if root != nil, delta is root-bound or state-bound, i.e. it is linked to a particular state.
		//   It can only be applied to that state, and it is guaranteed that it will always succeed
		// - if root == nil delta is not dependent on a particular baseline state and can be applied to any (with or without success)
		branchTxID *core.TransactionID
		// ledger coverage of the baseline state
		baselineCoverage uint64
	}
)

func (d utxoStateDelta) clone() utxoStateDelta {
	return util.CloneMapShallow(d)
}

func makeBuffered(d utxoStateDelta, buffered bool) *utxoStateDeltaBuffered {
	ret := &utxoStateDeltaBuffered{
		utxoStateDelta: d,
	}
	if buffered {
		ret.buffer = make(utxoStateDelta)
	}
	return ret
}

func (dc *utxoStateDeltaBuffered) isAlreadyIncluded(vid *WrappedTx, baselineState ...general.StateReader) bool {
	if _, alreadyIncluded := dc.get(vid); alreadyIncluded {
		return true
	}
	if len(baselineState) == 0 {
		return false
	}

	return baselineState[0].KnowsCommittedTransaction(vid.ID())
}

// consume does not mutate state in case of conflict
func (dc *utxoStateDeltaBuffered) consume(wOut WrappedOutput, baselineState ...general.StateReader) WrappedOutput {
	if consumedSet, found := dc.get(wOut.VID); found {
		// corresponding tx is already in the delta
		if consumedSet.Contains(wOut.Index) {
			return wOut
		}
		if len(baselineState) > 0 {
			if !baselineState[0].HasUTXO(wOut.DecodeID()) {
				// output is not in the state
				if wOut.VID.IsVirtualTx() {
					// virtual inputs must be in the state
					return wOut
				}
				if baselineState[0].KnowsCommittedTransaction(wOut.VID.ID()) {
					// transaction is committed but output is not in the state
					return wOut
				}
			}
		}
		// output exists and is not consumed
		consumedSet.Insert(wOut.Index)
		dc.put(wOut.VID, consumedSet)
		return WrappedOutput{}
	}

	// there's no corresponding tx in the delta
	if len(baselineState) > 0 {
		// output is in the state
		if baselineState[0].HasUTXO(wOut.DecodeID()) {
			dc.put(wOut.VID, set.NewByteSet(wOut.Index))
			return WrappedOutput{}
		}
	}
	// no baseline state provided or output is not in the baseline state (it is on the vertex)
	if conflict := dc.include(wOut.VID, baselineState...); conflict.VID != nil {
		return conflict
	}
	dc.put(wOut.VID, set.NewByteSet(wOut.Index))

	return WrappedOutput{}
}

func (dc *utxoStateDeltaBuffered) include(vid *WrappedTx, baselineState ...general.StateReader) (conflict WrappedOutput) {
	if dc.isAlreadyIncluded(vid, baselineState...) {
		return
	}
	for _, wOut := range vid.WrappedInputs() {
		// virtual tx has 0 WrappedInputs
		if conflict = dc.consume(wOut, baselineState...); conflict.VID != nil {
			return
		}
	}
	dc.put(vid, set.EmptyByteSet)
	return
}

// append baselineState must be the baseline state of d. d must be consistent with the baselineState
func (dc *utxoStateDeltaBuffered) append(delta utxoStateDelta, baselineState ...general.StateReader) (conflict WrappedOutput) {
	for vid := range delta {
		if conflict = dc.include(vid, baselineState...); conflict.VID != nil {
			return
		}
	}
	return
}

func (dc *utxoStateDeltaBuffered) get(vid *WrappedTx) (ret set.ByteSet, found bool) {
	if dc.buffer != nil {
		if ret, found = dc.buffer[vid]; found {
			return
		}
	}
	ret, found = dc.utxoStateDelta[vid]
	return
}

func (dc *utxoStateDeltaBuffered) put(vid *WrappedTx, consumedSet set.ByteSet) {
	if dc.buffer == nil {
		dc.utxoStateDelta[vid] = consumedSet
		return
	}
	dc.buffer[vid] = consumedSet
}

func (dc *utxoStateDeltaBuffered) flush() utxoStateDelta {
	if dc.buffer == nil {
		return dc.utxoStateDelta
	}
	ret := dc.utxoStateDelta
	for vid, consumedSet := range dc.buffer {
		ret[vid] = consumedSet
	}
	// invalidate
	dc.utxoStateDelta = nil
	dc.buffer = nil

	return ret
}

func (d utxoStateDelta) lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)

	sorted := util.SortKeys(d, func(vid1, vid2 *WrappedTx) bool {
		return vid1.Timestamp().Before(vid2.Timestamp())
	})
	for _, vid := range sorted {
		consumedSet := d[vid]
		ret.Add("%s consumed: %+v", vid.IDShort(), consumedSet.String())
	}
	return ret
}

func (d utxoStateDelta) Coverage() (ret uint64) {
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

func (d utxoStateDelta) getMutations(targetSlot core.TimeSlot, baselineState general.StateReader) *multistate.Mutations {
	ret := multistate.NewMutations()

	for vid, consumedSet := range d {
		if baselineState.KnowsCommittedTransaction(vid.ID()) {
			// do not produce anything if transaction is already in the state
			continue
		}
		// do not touch virtual transactions
		vid.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				// DEL mutations: deleting all inputs of transactions which are in the state
				v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
					inpID := v.Tx.MustInputAt(i)
					// TODO suboptimal check every input in the state
					if baselineState.HasUTXO(&inpID) {
						ret.InsertDelOutputMutation(inpID)
					}
					return true
				})
				// SET mutations: adding outputs of not-in-the-state state transaction which are not
				// marked as consumed. If all outputs are consumed, adding nothing
				v.Tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
					if !consumedSet.Contains(idx) {
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

func NewUTXOStateDelta(branchTxID *core.TransactionID) *UTXOStateDelta {
	return &UTXOStateDelta{
		utxoStateDelta: make(utxoStateDelta),
		branchTxID:     branchTxID,
	}
}

func (d *UTXOStateDelta) Clone() *UTXOStateDelta {
	return &UTXOStateDelta{
		utxoStateDelta: d.utxoStateDelta.clone(),
		branchTxID:     d.branchTxID,
	}
}

// Include inconsistent target delta in case of conflict
func (d *UTXOStateDelta) Include(vid *WrappedTx, getBaselineState func(branchTxID *core.TransactionID) general.StateReader) (ret WrappedOutput) {
	dc := makeBuffered(d.utxoStateDelta, false) // no cached
	if d.branchTxID != nil {
		return dc.include(vid, getBaselineState(d.branchTxID))
	}
	return dc.include(vid)
}

// Consume does not mutate delta in case of conflict
func (d *UTXOStateDelta) Consume(wOut WrappedOutput, getBaselineState func(branchTxID *core.TransactionID) general.StateReader) WrappedOutput {
	// do not buffer it because it mutates delta on in case of success
	dc := makeBuffered(d.utxoStateDelta, false)
	if d.branchTxID != nil {
		return dc.consume(wOut, getBaselineState(d.branchTxID))
	}
	return dc.consume(wOut)
}

// sortDeltas sorts deltas descending by baselineBranches, the latest are on top (if not nil)
// checks for conflicting baseline states
// the first in the sorted list will be the latest one, the others will be merged into it
func sortDeltas(deltas ...*UTXOStateDelta) ([]*UTXOStateDelta, bool) {
	ret := util.CloneArglistShallow(deltas...)
	sort.Slice(ret, func(i, j int) bool {
		bi := ret[i].branchTxID
		bj := ret[j].branchTxID
		switch {
		case bi != nil && bj == nil:
			return true
		case bi != nil && bj != nil:
			return bi.TimeSlot() > bj.TimeSlot()
		}
		return false
	})

	// check conflicting branches
	for i, d := range ret {
		if d.branchTxID == nil {
			break
		}
		if i+1 >= len(ret) {
			break
		}
		d1 := ret[i+1]
		if d1.branchTxID == nil {
			break
		}
		if d.branchTxID.TimeSlot() == d1.branchTxID.TimeSlot() && *d.branchTxID != *d1.branchTxID {
			// two different branches on the same slot conflicts
			return nil, false
		}
	}
	return ret, true
}

// MergeDeltas returns new copy of merged deltas. Arguments are not touched
func MergeDeltas(getStateReader func(branchTxID *core.TransactionID) general.StateReader, deltas ...*UTXOStateDelta) (*UTXOStateDelta, *WrappedOutput) {
	if len(deltas) == 0 {
		return &UTXOStateDelta{utxoStateDelta: make(utxoStateDelta)}, nil
	}
	if len(deltas) == 1 {
		return deltas[0].Clone(), nil
	}

	// find baseline state
	deltasSorted, ok := sortDeltas(deltas...)
	if !ok {
		return nil, &WrappedOutput{}
	}

	// deltasSorted are all non-conflicting and sorted descending by slot with nil-branches at the end
	var baselineStateArg []general.StateReader
	latestBranchTxID := deltasSorted[0].branchTxID
	if latestBranchTxID != nil {
		baselineStateArg = util.List(getStateReader(latestBranchTxID))
	}

	var conflict WrappedOutput
	var retTmp *utxoStateDeltaBuffered

	for i, d := range deltasSorted {
		if i == 0 {
			// here we clone the first and will merge the rest into it. No buffering
			retTmp = makeBuffered(deltasSorted[0].utxoStateDelta.clone(), false)
			continue
		}
		if conflict = retTmp.append(d.utxoStateDelta, baselineStateArg...); conflict.VID != nil {
			return nil, &conflict
		}
	}

	ret := retTmp.flush()

	return &UTXOStateDelta{
		utxoStateDelta: ret,
		branchTxID:     latestBranchTxID,
	}, nil
}

func MergeVertexDeltas(getStateReader func(branchTxID *core.TransactionID) general.StateReader, vids ...*WrappedTx) (*UTXOStateDelta, *WrappedOutput) {
	deltas := make([]*UTXOStateDelta, len(vids))
	for i, vid := range vids {
		deltas[i] = vid.GetUTXOStateDelta()
	}
	return MergeDeltas(getStateReader, deltas...)
}

// MergeDeltas merges other deltas into the receiver. In case of success, receiver d is mutated
// In case of conflict, d is inconsistent (buffer is not flushed)
func (d *UTXOStateDelta) MergeDeltas(getStateReader func(branchTxID *core.TransactionID) general.StateReader, deltas ...*UTXOStateDelta) *WrappedOutput {
	if len(deltas) == 0 {
		return nil
	}
	if d.branchTxID == nil {
		return &WrappedOutput{} // cannot merge into not-state bound delta
	}

	// d.branchTxID must be a dominating/latest branch
	deltasSorted, ok := sortDeltas(deltas...)
	if !ok {
		return &WrappedOutput{}
	}
	if deltasSorted[0].branchTxID != d.branchTxID && deltasSorted[0].branchTxID != nil {
		if deltasSorted[0].branchTxID.TimeSlot() > d.branchTxID.TimeSlot() {
			return &WrappedOutput{}
		}
	}

	// deltasSorted are all non-conflicting and sorted descending by slot with nil-branches at the end
	var baselineStateArg []general.StateReader
	latestBranchTxID := d.branchTxID
	if latestBranchTxID != nil {
		baselineStateArg = util.List(getStateReader(latestBranchTxID))
	}

	var conflict WrappedOutput
	ret := makeBuffered(d.utxoStateDelta, true)

	for _, d1 := range deltasSorted {
		if conflict = ret.append(d1.utxoStateDelta, baselineStateArg...); conflict.VID != nil {
			return &conflict
		}
	}
	ret.flush() // only flushed if no conflicts

	return nil
}

// MergeVertexDeltas merge deltas of vertices into the target delta.
// The target delta is not mutated in case of conflict
func (d *UTXOStateDelta) MergeVertexDeltas(getStateReader func(branchTxID *core.TransactionID) general.StateReader, vids ...*WrappedTx) *WrappedOutput {
	deltas := make([]*UTXOStateDelta, len(vids))
	for i, vid := range vids {
		deltas[i] = vid.GetUTXOStateDelta()
	}
	return d.MergeDeltas(getStateReader, deltas...)
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

const enableCheckDeltaConsistency = true

func (d *UTXOStateDelta) MustCheckConsistency(getStateReader ...func(branchTxID *core.TransactionID) general.StateReader) {
	if enableCheckDeltaConsistency {
		d._mustCheckConsistency(getStateReader...)
	}
}

func (d *UTXOStateDelta) _mustCheckConsistency(getStateReader ...func(branchTxID *core.TransactionID) general.StateReader) {
	stateProvided := len(getStateReader) > 0
	var stateReader general.StateReader
	if stateProvided {
		stateReader = getStateReader[0](d.branchTxID)
	}
	for vid := range d.utxoStateDelta {
		if vid.IsBranchTransaction() {
			continue
		}
		txid := vid.ID()
		vid.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
					if util.HasKey(d.utxoStateDelta, inp) {
						return true
					}
					if stateProvided {
						if stateReader.KnowsCommittedTransaction(txid) {
							return true
						}
					}
					util.Panicf("MustCheckConsistency: input %s of %s must be in the delta:\n%s",
						inp.IDShort(), vid.IDShort(), d.lines().String())
					return true
				})
			},
		})
	}
}
