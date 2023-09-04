package utangle

import (
	"sort"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/state"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

func NewUTXOStateDelta(baselineBranch *WrappedTx) *UTXOStateDelta {
	util.Assertf(baselineBranch == nil || baselineBranch.IsBranchTransaction(), "NewUTXOStateDelta: wrong base branch: %s", func() any {
		return baselineBranch.IDShort()
	})
	return &UTXOStateDelta{
		baselineBranch: baselineBranch,
		transactions:   make(map[*WrappedTx]transactionData),
	}
}

func (d *UTXOStateDelta) Clone() *UTXOStateDelta {
	ret := &UTXOStateDelta{
		baselineBranch: d.baselineBranch,
		transactions:   make(map[*WrappedTx]transactionData),
		coverage:       d.coverage,
	}
	for vid, td := range d.transactions {
		ret.transactions[vid] = transactionData{
			consumed:          util.CloneMapShallow(td.consumed),
			includedThisDelta: td.includedThisDelta,
		}
	}
	return ret
}

func (d *UTXOStateDelta) BaselineBranch() *WrappedTx {
	return d.baselineBranch
}

// getUpdateCommands generates state update commands for closed delta
func (d *UTXOStateDelta) getUpdateCommands() []state.UpdateCmd {
	ret := make([]state.UpdateCmd, 0)
	for vid, td := range d.transactions {
		if !td.includedThisDelta {
			continue
		}
		vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			v.Tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
				produce := false
				if td.consumed == nil {
					produce = true
				} else {
					if _, isConsumed := td.consumed[idx]; !isConsumed {
						produce = true
					}
				}
				if produce {
					ret = append(ret, state.UpdateCmd{
						ID:     oid,
						Output: o,
					})
				}
				return true
			})

			v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
				tdInp, ok := d.transactions[v.Inputs[i]]
				util.Assertf(ok, "getUpdateCommands: missing input %s in transaction %s", v.Inputs[i].IDShort(), v.Tx.IDShort())
				if !tdInp.includedThisDelta || v.Inputs[i].IsVirtualTx() {
					ret = append(ret, state.UpdateCmd{
						ID: oid,
					})
				}
				return true
			})
		}})
	}
	sort.Slice(ret, func(i, j int) bool {
		// first DELs than ADDs
		return ret[i].Output == nil && ret[j].Output != nil
	})
	return ret
}

// include includes transaction into the delta
func (d *UTXOStateDelta) include(vid *WrappedTx) (conflict *WrappedOutput) {
	if d.isIncluded(vid) {
		return nil
	}

	d.transactions[vid] = transactionData{includedThisDelta: true}

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
				wOut := WrappedOutput{
					VID:   v.Inputs[i],
					Index: oid.Index(),
				}
				success, decrementCoverage := d.MustConsume(wOut)
				if !success {
					conflict = &wOut
					return false
				}
				if decrementCoverage {
					o, err := v.Inputs[i].OutputAt(oid.Index())
					util.AssertNoError(err)
					util.Assertf(o != nil, "output %s not available", oid.Short())
					util.Assertf(d.coverage >= o.Amount(), "d.coverage >= o.Amount()")
					d.coverage -= o.Amount()
				}
				return true
			})
			d.coverage += uint64(v.Tx.TotalAmount())
		},
	})
	if conflict != nil {
		return conflict
	}
	return nil
}

func (d *UTXOStateDelta) traverseBack(fun func(dCur *UTXOStateDelta) bool) {
	for dCur, exit := d, false; !exit; {
		if !fun(dCur) {
			return
		}
		exit = true
		if prev := dCur.baselineBranch; prev != nil {
			prev.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
				dCur = &v.StateDelta
				exit = false
			}})
		}
	}
}

// isIncluded returns true if delta or one of its predecessor contains transaction
func (d *UTXOStateDelta) isIncluded(vid *WrappedTx) bool {
	already := false
	d.traverseBack(func(dCur *UTXOStateDelta) bool {
		_, already = dCur.transactions[vid]
		return !already
	})
	return already
}

// getConsumedSet returns:
// - consumed set either from the current delta (if tx exists) or inherited from the past
// - true/false if transaction was found at all
func (d *UTXOStateDelta) getConsumedSet(vid *WrappedTx) (ret set.Set[byte], txFound bool) {
	var td transactionData
	d.traverseBack(func(dCur *UTXOStateDelta) bool {
		if td, txFound = dCur.transactions[vid]; txFound {
			ret = td.consumed
		}
		return !txFound
	})
	return
}

func (d *UTXOStateDelta) CanBeConsumedBySequencer(wOut WrappedOutput, ut *UTXOTangle) bool {
	util.Assertf(d.baselineBranch != nil, "CanBeConsumedBySequencer: sequencer delta expected, baselineBranch must be not nil")

	canBeConsumed := false
	wOut.VID.Unwrap(UnwrapOptions{
		Vertex: func(_ *Vertex) {
			consumed, _ := d.getConsumedSet(wOut.VID) // if tx nt found can be consumed
			canBeConsumed = !consumed.Contains(wOut.Index)
		},
		VirtualTx: func(_ *VirtualTransaction) {
			rdr, ok := ut.StateReaderOfSequencerMilestone(d.baselineBranch)
			util.Assertf(ok, "CanBeConsumedBySequencer: cannot read state of branch %s", func() any { return d.baselineBranch.IDShort() })
			canBeConsumed = rdr.HasUTXO(wOut.DecodeID())
		},
	})
	return canBeConsumed
}

// MustConsume adds new consumed output record into the delta.
// Wrapped transaction of the output must be present in the delta, otherwise it panics.
// In case of conflict (double spend), returns false and does not modify receiver
// If success return flag if ledger coverage must be decremented by the amount of the output
func (d *UTXOStateDelta) MustConsume(wOut WrappedOutput) (bool, bool) {
	consumed, txFound := d.getConsumedSet(wOut.VID)
	util.Assertf(txFound, "UTXOStateDelta.MustConsume: transaction %s has not been found in the delta:\n%s",
		func() any { return wOut.VID.IDShort() },
		func() any { return d.LinesRecursive().String() },
	)

	if consumed != nil {
		if _, isConsumed := consumed[wOut.Index]; isConsumed {
			// conflict
			return false, false
		}
	}

	td := d.transactions[wOut.VID]
	if td.consumed == nil {
		// nil -> empty set
		td.consumed = set.New[byte]().AddAll(consumed)
	}

	td.consumed.Insert(wOut.Index)
	d.transactions[wOut.VID] = td

	decrementCoverage := td.includedThisDelta && wOut.VID.IsWrappedTx()
	return true, decrementCoverage
}

func (d *UTXOStateDelta) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	baseBranchName := "(nil)"
	if d.baselineBranch != nil {
		d.baselineBranch.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				baseBranchName = d.baselineBranch.IDShort() + "(wrappedTx)"
			},
			VirtualTx: func(v *VirtualTransaction) {
				baseBranchName = d.baselineBranch.IDShort() + "(virtual Tx)"
			},
		})
	}

	ret.Add("    Delta base: %s, num tx: %d", baseBranchName, len(d.transactions))
	for _, vid := range d.orderedTransactions() {
		td := d.transactions[vid]
		c := td.consumed.Ordered(func(el1, el2 byte) bool {
			return el1 < el2
		})
		ret.Add("        %s (this delta: %v, consumed: %+v)", vid.IDShort(), td.includedThisDelta, c)
	}
	return ret
}

func (d *UTXOStateDelta) String() string {
	return d.Lines().String()
}

func (d *UTXOStateDelta) LinesRecursive(prefix ...string) *lines.Lines {
	ret := lines.New()

	d.traverseBack(func(dCur *UTXOStateDelta) bool {
		ret.Append(dCur.Lines(prefix...))
		return true
	})
	return ret
}

// collectDeltasDesc past deltas sorted by slot
func (d *UTXOStateDelta) pastDeltas() map[core.TimeSlot]*UTXOStateDelta {
	if d.baselineBranch == nil {
		return make(map[core.TimeSlot]*UTXOStateDelta)
	}
	var ret map[core.TimeSlot]*UTXOStateDelta
	d.baselineBranch.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.StateDelta.pastDeltas()
		ret[v.TimeSlot()] = d
	}})
	return ret
}

// orderedTransactions sorting by timestamp is equivalent to topological sorting by DAG
func (d *UTXOStateDelta) orderedTransactions() []*WrappedTx {
	return util.SortKeys(d.transactions, func(vid1, vid2 *WrappedTx) bool {
		return vid1.Timestamp().Before(vid2.Timestamp())
	})
}

// _mergeInto return conflicting output and the transaction which was trying to include
func (d *UTXOStateDelta) _mergeInto(target *UTXOStateDelta) (*WrappedOutput, *WrappedTx) {
	for _, vid := range d.orderedTransactions() {
		if conflict := target.include(vid); conflict != nil {
			return conflict, vid
		}
	}
	return nil, nil
}

func (d *UTXOStateDelta) MergeInto(target *UTXOStateDelta) (*WrappedOutput, *WrappedTx) {
	// list all deltas until the one which has baselineBranch included into the target
	deltasToMerge := make([]*UTXOStateDelta, 0)
	d.traverseBack(func(dCur *UTXOStateDelta) bool {
		util.Assertf(dCur != nil, "dCur != nil")
		deltasToMerge = append(deltasToMerge, dCur)
		if dCur.baselineBranch == nil || target.isIncluded(dCur.baselineBranch) {
			return false
		}
		return true
	})

	var conflict *WrappedOutput
	var consumer *WrappedTx
	util.RangeReverse(deltasToMerge, func(_ int, dCur *UTXOStateDelta) bool {
		conflict, consumer = dCur._mergeInto(target)
		return conflict == nil
	})
	return conflict, consumer
}

func (d *UTXOStateDelta) ledgerCoverage(nSlotsBack int) uint64 {
	ret := uint64(0)
	d.traverseBack(func(dCur *UTXOStateDelta) bool {
		ret += dCur.coverage
		nSlotsBack--
		return nSlotsBack > 0
	})
	return ret
}
