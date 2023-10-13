package utangle

import (
	"sort"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type (
	UTXOStateDelta struct {
		utxoStateDelta
		// baseline state root.
		// - if root != nil, delta is root-bound or state-bound, i.e. it is linked to a particular state.
		//   It can only bne applied to that state, and it is guaranteed that it will always succeed
		// - if root == nil delta is not dependent on a particular baseline state and can be applied to any (with or without success)
		branchTxID *core.TransactionID
	}
)

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
