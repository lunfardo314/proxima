package utangle

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/atomic"
)

type (
	VertexType byte

	// WrappedTx value of *WrappedTx is used as transaction code in the UTXO tangle
	WrappedTx struct {
		mutex sync.RWMutex
		_genericWrapper
	}

	WrappedOutput struct {
		VID   *WrappedTx
		Index byte
	}

	_genericWrapper interface {
		_id() *core.TransactionID
		_time() time.Time
		_outputAt(idx byte) (*core.Output, error)
		_hasOutputAt(idx byte) (bool, bool)
		_mustNotUnwrapped()
		_toggleUnwrapped()
	}

	_vertex struct {
		*Vertex
		whenWrapped time.Time
		unwrapped   atomic.Bool
	}

	_virtualTx struct {
		*VirtualTransaction
		unwrapped atomic.Bool
	}

	_orphanedTx struct {
		core.TransactionID
		unwrapped atomic.Bool
	}

	UnwrapOptions struct {
		Vertex    func(v *Vertex)
		VirtualTx func(v *VirtualTransaction)
		Orphaned  func()
	}

	UnwrapOptionsForTraverse struct {
		Vertex    func(vidCur *WrappedTx, v *Vertex) bool
		VirtualTx func(vidCur *WrappedTx, v *VirtualTransaction) bool
		Orphaned  func(vidCur *WrappedTx) bool
	}
)

func (v _vertex) _id() *core.TransactionID {
	return v.Tx.ID()
}

func (v _vertex) _time() time.Time {
	return v.whenWrapped
}

func (v _vertex) _outputAt(idx byte) (*core.Output, error) {
	return v.Tx.ProducedOutputAt(idx)
}

func (v _vertex) _hasOutputAt(idx byte) (bool, bool) {
	if int(idx) >= v.Tx.NumProducedOutputs() {
		return false, true
	}
	return true, false
}

func (v _vertex) _mustNotUnwrapped() {
	util.Assertf(!v.unwrapped.Load(), "nested unwrapping not allowed")
}

func (v _vertex) _toggleUnwrapped() {
	v.unwrapped.Toggle()
}

func (v _virtualTx) _id() *core.TransactionID {
	return &v.txid
}

func (v _virtualTx) _time() time.Time {
	return time.Time{}
}

func (v _virtualTx) _outputAt(idx byte) (*core.Output, error) {
	if o, available := v.OutputAt(idx); available {
		return o, nil
	}
	return nil, nil
}

func (v _virtualTx) _hasOutputAt(idx byte) (bool, bool) {
	_, hasIt := v.OutputAt(idx)
	return hasIt, false
}

func (v _virtualTx) _mustNotUnwrapped() {
	util.Assertf(!v.unwrapped.Load(), "nested unwrapping not allowed")
}

func (v _virtualTx) _toggleUnwrapped() {
	v.unwrapped.Toggle()
}

func (v _orphanedTx) _id() *core.TransactionID {
	return &v.TransactionID
}

func (v _orphanedTx) _time() time.Time {
	return time.Time{}
}

func (v _orphanedTx) _outputAt(_ byte) (*core.Output, error) {
	panic("orphaned vertex should not be accessed")
}

func (v _orphanedTx) _hasOutputAt(idx byte) (bool, bool) {
	panic("orphaned vertex should not be accessed")
}

func (v _orphanedTx) _mustNotUnwrapped() {
	util.Assertf(!v.unwrapped.Load(), "nested unwrapping not allowed")
}

func (v _orphanedTx) _toggleUnwrapped() {
	v.unwrapped.Toggle()
}

func _newVID(g _genericWrapper) *WrappedTx {
	return &WrappedTx{_genericWrapper: g}
}

func (vid *WrappedTx) _put(g _genericWrapper) {
	vid._genericWrapper = g
}

func (vid *WrappedTx) ID() *core.TransactionID {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid._genericWrapper._id()
}

func DecodeIDs(vids ...*WrappedTx) []*core.TransactionID {
	ret := make([]*core.TransactionID, len(vids))
	for i, vid := range vids {
		ret[i] = vid.ID()
	}
	return ret
}

func (vid *WrappedTx) IDShort() string {
	return vid.ID().Short()
}

func (vid *WrappedTx) LazyIDShort() func() any {
	return func() any {
		return vid.IDShort()
	}
}

func (vid *WrappedTx) IDVeryShort() string {
	return vid.ID().VeryShort()
}

func (vid *WrappedTx) IsBranchTransaction() bool {
	return vid.ID().BranchFlagON()
}

func (vid *WrappedTx) IsSequencerMilestone() bool {
	return vid.ID().SequencerFlagON()
}

func (vid *WrappedTx) Timestamp() core.LogicalTime {
	return vid._genericWrapper._id().Timestamp()
}

func (vid *WrappedTx) TimeSlot() core.TimeSlot {
	return vid._genericWrapper._id().TimeSlot()
}

func (vid *WrappedTx) MarkOrphaned() {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			vid._put(_orphanedTx{TransactionID: *v.Tx.ID()})
		},
		VirtualTx: func(v *VirtualTransaction) {
			vid._put(_orphanedTx{TransactionID: v.txid})
		},
	})
}

func (vid *WrappedTx) OutputWithIDAt(idx byte) (*core.OutputWithID, error) {
	ret, err := vid.OutputAt(idx)
	if err != nil || ret == nil {
		return nil, err
	}
	return &core.OutputWithID{
		ID:     core.NewOutputID(vid.ID(), idx),
		Output: ret,
	}, nil
}

// OutputAt return output at index, if available.
// err != nil indicates wrong index
// nil, nil means output not available, but no error (orphaned)
func (vid *WrappedTx) OutputAt(idx byte) (*core.Output, error) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid._outputAt(idx)
}

func (vid *WrappedTx) HasOutputAt(idx byte) (bool, bool) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid._hasOutputAt(idx)
}

func (vid *WrappedTx) SequencerIDIfAvailable() (core.ChainID, bool) {
	var isAvailable bool
	var ret core.ChainID
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			isAvailable = v.Tx.IsSequencerMilestone()
			if isAvailable {
				ret = v.Tx.SequencerTransactionData().SequencerID
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			if v.sequencerOutputs != nil {
				seqOData, ok := v.outputs[v.sequencerOutputs[0]].SequencerOutputData()
				util.Assertf(ok, "sequencer output data unavailable for the output #%d", v.sequencerOutputs[0])
				ret = seqOData.ChainConstraint.ID
				if ret == core.NilChainID {
					oid := vid.OutputID(v.sequencerOutputs[0])
					ret = core.OriginChainID(&oid)
				}
				isAvailable = true
			}
		},
	})
	return ret, isAvailable
}

func (vid *WrappedTx) MustSequencerID() core.ChainID {
	ret, ok := vid.SequencerIDIfAvailable()
	util.Assertf(ok, "not a sequencer milestone")
	return ret
}

func (vid *WrappedTx) SequencerPredecessor() *WrappedTx {
	util.Assertf(vid.IsSequencerMilestone(), "SequencerPredecessor: %s is not a sequencer milestone", func() any { return vid.IDShort() })

	var ret *WrappedTx
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.Inputs[v.Tx.SequencerTransactionData().SequencerOutputData.ChainConstraint.PredecessorInputIndex]
	}})
	return ret
}

func (vid *WrappedTx) MustSequencerOutput() *WrappedOutput {
	if !vid.IsSequencerMilestone() {
		return nil
	}
	ret := &WrappedOutput{
		VID: vid,
	}
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret.Index = v.Tx.SequencerTransactionData().SequencerOutputIndex
		},
		VirtualTx: func(v *VirtualTransaction) {
			util.Assertf(v.sequencerOutputs != nil, "sequencer output data not available in virtual tx %s", v.txid.Short())
			ret.Index = v.sequencerOutputs[0]
		},
	})
	return ret
}

func (vid *WrappedTx) StemOutput() *WrappedOutput {
	if !vid.IsBranchTransaction() {
		return nil
	}
	ret := &WrappedOutput{
		VID: vid,
	}
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret.Index = v.Tx.SequencerTransactionData().StemOutputIndex
		},
		VirtualTx: func(v *VirtualTransaction) {
			if v.sequencerOutputs != nil {
				ret.Index = v.sequencerOutputs[1]
			} else {
				ret = nil
			}
		},
	})
	return ret
}

func (vid *WrappedTx) BaseStemOutput() *WrappedOutput {
	panic("not implemented")
	//var ret *WrappedOutput
	//isBranchTx := vid.IsBranchTransaction()
	//vid.Unwrap(UnwrapOptions{
	//	Vertex: func(v *Vertex) {
	//		if isBranchTx {
	//			ret = &WrappedOutput{
	//				VID:   vid,
	//				Index: v.Tx.SequencerTransactionData().StemOutputIndex,
	//			}
	//		} else {
	//			if b := v.StateDelta.BaselineBranch(); b != nil {
	//				util.Assertf(b.IsBranchTransaction(), "%s is not a branch transaction", b.IDShort())
	//				ret = b.StemOutput()
	//			}
	//		}
	//	},
	//	VirtualTx: func(v *VirtualTransaction) {
	//		if isBranchTx {
	//			if _, stemOut := v.SequencerOutputs(); stemOut != nil {
	//				ret = &WrappedOutput{
	//					VID:   vid,
	//					Index: v.sequencerOutputs[1],
	//				}
	//			}
	//		}
	//	},
	//})
	//return ret
}

func (vid *WrappedTx) UnwrapVertex() (ret *Vertex, retOk bool) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v
			retOk = true
		},
	})
	return
}

func (vid *WrappedTx) UnwrapTransaction() *transaction.Transaction {
	var ret *transaction.Transaction
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.Tx
	}})
	return ret
}

func (vid *WrappedTx) IsVirtualTx() (ret bool) {
	vid.Unwrap(UnwrapOptions{VirtualTx: func(_ *VirtualTransaction) {
		ret = true
	}})
	return
}

func (vid *WrappedTx) IsWrappedTx() (ret bool) {
	vid.Unwrap(UnwrapOptions{Vertex: func(_ *Vertex) {
		ret = true
	}})
	return
}

func (vid *WrappedTx) IsOrphaned() bool {
	ret := false
	vid.Unwrap(UnwrapOptions{
		Orphaned: func() {
			ret = true
		},
	})
	return ret
}

func (vid *WrappedTx) OutputID(idx byte) (ret core.OutputID) {
	ret = core.NewOutputID(vid.ID(), idx)
	return
}

type _unwrapOptionsTraverse struct {
	UnwrapOptionsForTraverse
	visited set.Set[*WrappedTx]
}

const trackNestedUnwraps = true

func (vid *WrappedTx) Unwrap(opt UnwrapOptions) {
	// to trace possible deadlocks in case of nested unwrapping
	if trackNestedUnwraps {
		vid._mustNotUnwrapped()
	}

	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	switch v := vid._genericWrapper.(type) {
	case _vertex:
		if opt.Vertex != nil {
			if trackNestedUnwraps {
				vid._toggleUnwrapped()
			}
			opt.Vertex(v.Vertex)
			if trackNestedUnwraps {
				vid._toggleUnwrapped()
			}
		}
	case _virtualTx:
		if opt.VirtualTx != nil {
			if trackNestedUnwraps {
				vid._toggleUnwrapped()
			}
			opt.VirtualTx(v.VirtualTransaction)
			if trackNestedUnwraps {
				vid._toggleUnwrapped()
			}
		}
	case _orphanedTx:
		if opt.Orphaned != nil {
			if trackNestedUnwraps {
				vid._toggleUnwrapped()
			}
			opt.Orphaned()
			if trackNestedUnwraps {
				vid._toggleUnwrapped()
			}
		}
	default:
		util.Assertf(false, "inconsistency: unsupported wrapped type")
	}
}

func (vid *WrappedTx) Time() time.Time {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid._genericWrapper._time()
}

// TODO temporary

func (vid *WrappedTx) LedgerCoverage() (ret uint64) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.StateDelta.coverage()
		},
	})
	return // vid._ledgerCoverage(LedgerCoverageSlots)
}

//
//func (vid *WrappedTx) _ledgerCoverage(nSlotsBack int) uint64 {
//	if !vid.IsSequencerMilestone() {
//		return 0
//	}
//	if vid.IsBranchTransaction() {
//		// this is needed to make baselines of comparison between branch and non-branch milestones equal
//		nSlotsBack--
//	}
//	var ret uint64
//	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
//		ret = v.StateDelta.ledgerCoverage(nSlotsBack)
//	}})
//	return ret
//}

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
			v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
				util.Assertf(inp != nil, "_traversePastCone: input %d is nil (not solidified) in %s",
					i, func() any { return v.Tx.IDShort() })
				ret = inp._traversePastCone(opt)
				return ret
			})
			if ret {
				v.forEachEndorsement(func(i byte, inpEnd *WrappedTx) bool {
					util.Assertf(inpEnd != nil, "_traversePastCone: endorsement %d is nil (not solidified) in %s",
						i, func() any { return v.Tx.IDShort() })
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
		Orphaned: func() {
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

func (vid *WrappedTx) StartNextSequencerMilestoneDelta(other ...*WrappedTx) (*UTXOStateDelta, *WrappedOutput, *WrappedTx) {
	panic("no implemented")
	//util.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")
	//
	//var ret *UTXOStateDelta
	//vid.Unwrap(UnwrapOptions{
	//	Vertex: func(v *Vertex) {
	//		ret = v.StateDelta.Clone()
	//	},
	//	VirtualTx: func(_ *VirtualTransaction) {
	//		ret = NewUTXOStateDelta(vid)
	//	},
	//})
	//if ret == nil {
	//	// orphaned
	//	return nil, nil, nil
	//}
	//var conflict *WrappedOutput
	//var consumer *WrappedTx
	//
	//var notOrphaned bool
	//
	//for _, seqVID := range other {
	//	seqVID.Unwrap(UnwrapOptions{
	//		Vertex: func(v *Vertex) {
	//			conflict, consumer = v.StateDelta.MergeInto(ret)
	//			notOrphaned = true
	//		},
	//		VirtualTx: func(v *VirtualTransaction) {
	//			notOrphaned = true
	//		},
	//	})
	//	if conflict != nil {
	//		return nil, conflict, consumer
	//	}
	//	if !notOrphaned {
	//		return nil, nil, nil
	//	}
	//}
	//return ret, nil, nil
}

func (vid *WrappedTx) String() string {
	return vid.Lines().String()
}

func (vid *WrappedTx) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret.Add("== vertex %s", vid.IDShort())
			ret.Append(v.Lines(prefix...))
		},
		VirtualTx: func(v *VirtualTransaction) {
			ret.Add("== virtual tx %s", vid.IDShort())
			idxs := util.SortKeys(v.outputs, func(k1, k2 byte) bool {
				return k1 < k2
			})
			for _, i := range idxs {
				ret.Add("    #%d :", i)
				ret.Append(v.outputs[i].ToLines("     "))
			}
		},
		Orphaned: func() {
			ret.Add("== orphaned vertex")
		},
	})
	return ret
}

func (vid *WrappedTx) DeltaString(skipCommands ...bool) string {
	ret := lines.New().Add("== delta of %s", vid.IDShort())
	skipCmd := len(skipCommands) > 0 && skipCommands[0]
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret.Append(v.StateDelta.Lines())
			if !skipCmd {
				cmds := v.StateDelta.getUpdateCommands()
				ret.Add("== update commands of %s (%d commands)", vid.IDShort(), len(cmds))
				ret.Append(multistate.UpdateCommandsToLines(cmds))
			}
		}, VirtualTx: func(v *VirtualTransaction) {
			ret.Add("   (virtualTx)")
		}, Orphaned: func() {
			ret.Add("   (orphanedTx)")
		},
	})
	return ret.String()
}

func (vid *WrappedTx) NumInputs() int {
	ret := 0
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.Tx.NumInputs()
	}})
	return ret
}

func (vid *WrappedTx) NumProducedOutputs() int {
	ret := 0
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.Tx.NumProducedOutputs()
	}})
	return ret
}

func (o *WrappedOutput) DecodeID() *core.OutputID {
	if o.VID == nil {
		ret := core.NewOutputID(&core.TransactionID{}, o.Index)
		return &ret
	}
	ret := o.VID.OutputID(o.Index)
	return &ret
}

func (o *WrappedOutput) IDShort() string {
	if o == nil {
		return "<nil>"
	}
	return o.DecodeID().Short()
}

func (o *WrappedOutput) Unwrap() (ret *core.OutputWithID, err error) {
	return o.VID.OutputWithIDAt(o.Index)
}

func (o *WrappedOutput) Timestamp() core.LogicalTime {
	return o.VID.Timestamp()
}

func (o *WrappedOutput) TimeSlot() core.TimeSlot {
	return o.VID.TimeSlot()
}

func (vid *WrappedTx) ConvertToVirtualTx() {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			vid._put(_virtualTx{VirtualTransaction: v.convertToVirtualTx()})
		},
		Orphaned: func() {
			panic("ConvertToVirtualTx: orphaned should not be accessed")
		},
	})
}

func (vid *WrappedTx) WrappedInputs() []WrappedOutput {
	ret := make([]WrappedOutput, vid.NumInputs())
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
			inpID := v.Tx.MustInputAt(i)
			ret[i] = WrappedOutput{
				VID:   inp,
				Index: inpID.Index(),
			}
			return true
		})
	}})
	return ret
}

func (vid *WrappedTx) BaseBranchTXID() (ret *core.TransactionID) {
	if vid.IsBranchTransaction() {
		ret = vid.ID()
		return
	}
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.StateDelta.branchTxID
	}})
	return
}

// GetUTXOStateDelta returns a pointer, not to be mutated, must be cloned first! Never nil
// For branch it returns empty delta with the branch as baseline
func (vid *WrappedTx) GetUTXOStateDelta() (ret *UTXOStateDelta) {
	if vid.IsBranchTransaction() {
		return NewUTXOStateDelta2(vid.BaseBranchTXID())
	}
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = &v.StateDelta
		},
		VirtualTx: func(v *VirtualTransaction) {
			ret = NewUTXOStateDelta2(nil)
		},
		Orphaned: func() {
			util.Panicf("orphaned vertex should not be accesses")
		},
	})
	return
}
