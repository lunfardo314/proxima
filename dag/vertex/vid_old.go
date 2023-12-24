package vertex

import (
	"bytes"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/dag/utangle"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

func (vid *WrappedTx) UnwrapTransaction() *transaction.Transaction {
	var ret *transaction.Transaction
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.Tx
	}})
	return ret
}

func (vid *WrappedTx) IsDeleted() (ret bool) {
	vid.Unwrap(UnwrapOptions{Deleted: func() {
		ret = true
	}})
	return
}

type _unwrapOptionsTraverse struct {
	UnwrapOptionsForTraverse
	visited set.Set[*WrappedTx]
}

// TraversePastConeDepthFirst performs depth-first traverse of the DAG. Visiting once each node
// and calling vertex-type specific function if provided on each.
// If function returns false, the traverse is cancelled globally.
// The traverse stops at terminal vertices. The vertex is terminal if it either is not-full vertex
// i.e. (booked, orphaned, deleted) or it belongs to 'visited' set
// If 'visited' set is provided at call, it is mutable. In the end it contains all initial vertices plus
// all vertices visited during the traverse
func (vid *WrappedTx) TraversePastConeDepthFirst(opt UnwrapOptionsForTraverse, visited ...set.Set[*WrappedTx]) {
	var visitedSet set.Set[*WrappedTx]
	if len(visited) > 0 {
		visitedSet = visited[0]
	} else {
		visitedSet = set.New[*WrappedTx]()
	}
	vid._traversePastCone(&_unwrapOptionsTraverse{
		UnwrapOptionsForTraverse: opt,
		visited:                  visitedSet,
	})
}

func (vid *WrappedTx) _traversePastCone(opt *_unwrapOptionsTraverse) bool {
	if opt.visited.Contains(vid) {
		return true
	}
	opt.visited.Insert(vid)

	ret := true
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.ForEachInputDependency(func(i byte, inp *WrappedTx) bool {
				util.Assertf(inp != nil, "_traversePastCone: input %d is nil (not solidified) in %s",
					i, func() any { return v.Tx.IDShortString() })
				ret = inp._traversePastCone(opt)
				return ret
			})
			if ret {
				v.ForEachEndorsement(func(i byte, inpEnd *WrappedTx) bool {
					util.Assertf(inpEnd != nil, "_traversePastCone: endorsement %d is nil (not solidified) in %s",
						i, func() any { return v.Tx.IDShortString() })
					ret = inpEnd._traversePastCone(opt)
					return ret
				})
			}
			if ret && opt.Vertex != nil {
				ret = opt.Vertex(vid, v)
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			if opt.VirtualTx != nil {
				ret = opt.VirtualTx(vid, v)
			}
		},
		Deleted: func() {
			if opt.Orphaned != nil {
				ret = opt.Orphaned(vid)
			}
		},
	})
	return ret
}

func (vid *WrappedTx) PastConeSet() set.Set[*WrappedTx] {
	ret := set.New[*WrappedTx]()
	vid.TraversePastConeDepthFirst(UnwrapOptionsForTraverse{}, ret)
	return ret
}

func (vid *WrappedTx) attachAsEndorser(endorser *WrappedTx) {
	if len(vid.endorsers) == 0 {
		vid.endorsers = make([]*WrappedTx, 0)
	}
	vid.endorsers = util.AppendUnique(vid.endorsers, endorser)
}

// _collectCoveredOutputs recursively goes back and collect all inputs/leafs which are contained in the baselineStateReader
// The coveredOutputs set will contain all outputs which were consumed from the baseline state
func (vid *WrappedTx) _collectCoveredOutputs(baselineStateReader global.StateReader, visited set.Set[*WrappedTx], coveredOutputs set.Set[WrappedOutput]) {
	if visited.Contains(vid) {
		return
	}
	visited.Insert(vid)

	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		v.ForEachInputDependency(func(i byte, inp *WrappedTx) bool {
			wInp := WrappedOutput{
				VID:   inp,
				Index: v.Tx.MustOutputIndexOfTheInput(i),
			}
			if baselineStateReader.HasUTXO(wInp.DecodeID()) {
				coveredOutputs.Insert(wInp)
			} else {
				inp._collectCoveredOutputs(baselineStateReader, visited, coveredOutputs)
			}
			return true
		})
		v.ForEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
			vEnd._collectCoveredOutputs(baselineStateReader, visited, coveredOutputs)
			return true
		})
	}})
	return
}

func (vid *WrappedTx) CoverageDelta(ut *utangle.UTXOTangle) (*core.TransactionID, uint64) {
	if ut == nil {
		// TODO temporary
		return nil, 0
	}
	baselineBranchVID := vid.BaselineBranch()
	if baselineBranchVID == nil {
		return nil, 0
	}

	baselineTxID := baselineBranchVID.ID()
	coveredOutputs := set.New[WrappedOutput]()
	vid._collectCoveredOutputs(ut.MustGetStateReader(baselineTxID), set.New[*WrappedTx](), coveredOutputs)

	ret := uint64(0)
	bd, found := multistate.FetchBranchData(ut.StateStore(), *baselineTxID)
	util.Assertf(found, "can't found root record for %s", baselineTxID.StringShort())
	coverageCap := bd.Stem.Output.MustStemLock().Supply

	coveredOutputs.ForEach(func(o WrappedOutput) bool {
		ret += o.Amount()
		return true
	})
	if ret > coverageCap {
		baselineOutputsLines := coveredOutputs.Lines(func(key WrappedOutput) string {
			return key.IDShort()
		})
		utangle.SaveGraphPastCone(vid, "failed_coverage")
		util.Panicf("CoverageDelta inconsistency: result for vertex %s = %s -> exceeds current total supply %s. Outputs summed up:\n%s",
			vid.IDShortString(), util.GoThousands(ret), util.GoThousands(coverageCap), baselineOutputsLines.String(),
		)
	}
	return baselineTxID, ret
}

func (vid *WrappedTx) InflationAmount() (ret uint64) {
	if !vid.IsBranchTransaction() {
		return
	}
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.Tx.InflationAmount()
		},
		VirtualTx: func(v *VirtualTransaction) {
			_, stemOut := v.SequencerOutputs()
			util.Assertf(stemOut != nil, "can't get stem output")
			lck, ok := stemOut.StemLock()
			util.Assertf(ok, "can't get stem output")
			ret = lck.InflationAmount
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return
}

func (vid *WrappedTx) Less(vid1 *WrappedTx) bool {
	return bytes.Compare(vid.ID()[:], vid1.ID()[:]) < 0
}

func (o *WrappedOutput) Less(o1 *WrappedOutput) bool {
	if o.VID == o1.VID {
		return o.Index < o1.Index
	}
	return o.VID.Less(o1.VID)
}

func (vid *WrappedTx) PastTrackLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)

	ret.Add("==== BEGIN forks of %s", vid.IDShortString())
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		v.ForEachInputDependency(func(i byte, inp *WrappedTx) bool {
			input := v.Tx.MustInputAt(i)
			if inp == nil {
				ret.Add("  INPUT %d : %s (not solid)", i, input.StringShort())
			} else {
				ret.Add("  INPUT %d : %s ", i, input.StringShort())
				inp.Unwrap(UnwrapOptions{Vertex: func(vInp *Vertex) {
					ret.Append(vInp.pastTrack.Lines("      "))
				}})
			}
			return true
		})
		v.ForEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
			ret.Add("  ENDORSEMENT %d : %s ", i, vEnd.IDShortString())
			vEnd.Unwrap(UnwrapOptions{Vertex: func(vEnd *Vertex) {
				ret.Append(vEnd.pastTrack.Lines("     "))
			}})
			return true
		})
		ret.Add("  MERGED:")
		ret.Append(v.pastTrack.Lines("      "))
	}})
	ret.Add("==== END forks of %s", vid.IDShortString())
	return ret
}

func MergePastTracks(getStore func() global.StateStore, vids ...*WrappedTx) (ret PastTrack, conflict *WrappedOutput) {
	if len(vids) == 0 {
		return
	}

	retTmp := newPastTrack()
	for _, vid := range vids {
		conflict = retTmp.absorbPastTrack(vid, getStore)
		if conflict != nil {
			return
		}
	}
	ret = retTmp
	return
}

func (o *WrappedOutput) IsConsumed(tips ...*WrappedTx) bool {
	if len(tips) == 0 {
		return false
	}
	visited := set.New[*WrappedTx]()

	consumed := false
	for _, tip := range tips {
		if consumed = o._isConsumedInThePastConeOf(tip, visited); consumed {
			break
		}
	}
	return consumed
}

func (o *WrappedOutput) _isConsumedInThePastConeOf(vid *WrappedTx, visited set.Set[*WrappedTx]) (consumed bool) {
	if visited.Contains(vid) {
		return
	}
	visited.Insert(vid)

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.ForEachInputDependency(func(i byte, vidInput *WrappedTx) bool {
				if o.VID == vidInput {
					consumed = o.Index == v.Tx.MustOutputIndexOfTheInput(i)
				} else {
					consumed = o._isConsumedInThePastConeOf(vidInput, visited)
				}
				return !consumed
			})
			if !consumed {
				v.ForEachEndorsement(func(_ byte, vidEndorsed *WrappedTx) bool {
					consumed = o._isConsumedInThePastConeOf(vidEndorsed, visited)
					return !consumed
				})
			}
		},
	})
	return
}

func (o *WrappedOutput) ValidPace(targetTs core.LogicalTime) bool {
	return core.ValidTimePace(o.Timestamp(), targetTs)
}

func (vid *WrappedTx) ConsumersNoLock(outputIndex byte) []*WrappedTx {
	return vid.consumers[outputIndex]
}

func (vid *WrappedTx) propagateNewConflictSet(wOut *WrappedOutput, visited set.Set[*WrappedTx]) {
	if vid.IsDeleted() {
		return
	}
	if visited.Contains(vid) {
		return
	}
	visited.Insert(vid)

	for _, consumers := range vid.consumers {
		for _, descendant := range consumers {
			descendant.propagateNewConflictSet(wOut, visited)
		}
	}
	for _, vidEndorser := range vid.endorsers {
		vidEndorser.propagateNewConflictSet(wOut, visited)
	}
	ok := vid.addFork(newFork(*wOut, 0))
	util.Assertf(ok, "unexpected conflict while propagating new fork")
}
