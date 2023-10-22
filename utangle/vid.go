package utangle

import (
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
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
		descendants set.Set[*WrappedTx] // protected under global utangle lock
		consumers   map[byte]uint16
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
	return &WrappedTx{
		_genericWrapper: g,
	}
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

func (vid *WrappedTx) SequencerPredecessor() (ret *WrappedTx) {
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		if seqData := v.Tx.SequencerTransactionData(); seqData != nil {
			ret = v.Inputs[seqData.SequencerOutputData.ChainConstraint.PredecessorInputIndex]
		}
	}})
	return
}

// SequencerPastPath collects all path of the chain back to the first not nil (wrapped tx)
// ordered descending in time
func (vid *WrappedTx) SequencerPastPath() []*WrappedTx {
	ret := make([]*WrappedTx, 0)
	for vid1 := vid; vid1 != nil; vid1 = vid1.SequencerPredecessor() {
		ret = append(ret, vid1)
	}
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

func (vid *WrappedTx) BaselineStateOfSequencerMilestone(ut *UTXOTangle) (general.IndexedStateReader, error) {
	branchTxID := vid.DeltaBranchVID()
	if branchTxID == nil {
		return nil, fmt.Errorf("branch transaction not available")
	}
	return ut.GetIndexedStateReader(branchTxID.ID())
}

// BaseStemOutput returns wrapped stem output for the branch state or nil if unavailable
func (vid *WrappedTx) BaseStemOutput(ut *UTXOTangle) *WrappedOutput {
	branchVID := vid.DeltaBranchVID()
	if branchVID == nil {
		return nil
	}
	oid, ok := multistate.FetchStemOutputID(ut.stateStore, *branchVID.ID())
	if !ok {
		return nil
	}
	ret, found, invalid := ut.GetWrappedOutput(&oid)
	util.Assertf(found && !invalid, "found & !invalid")

	return &ret
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

func (vid *WrappedTx) MustUnwrapVertex() *Vertex {
	ret, ok := vid.UnwrapVertex()
	util.Assertf(ok, "must be a Vertex")
	return ret
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

func (vid *WrappedTx) LinesOfInputDeltas(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("=== delta lines of %s START", vid.IDShort())
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
			oid := v.Tx.MustInputAt(i)
			if inp == nil {
				ret.Add("INPUT %2d: %s : not solid", i, oid.Short())
				return true
			}
			ret.Add("INPUT %2d: %s", i, oid.Short())
			ret.Append(v.Inputs[i].GetUTXOStateDelta().Lines("   "))
			return true
		})
		v.forEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
			txid := v.Tx.EndorsementAt(i)
			if vEnd == nil {
				ret.Add("ENDORSE %2d: %s : not solid", i, txid.Short())
				return true
			}
			ret.Add("ENDORSE %2d: %s", i, txid.Short())
			ret.Append(vEnd.GetUTXOStateDelta().Lines("   "))
			return true
		})
	}})
	ret.Add("=== delta lines of %s END", vid.IDShort())
	return ret
}

func (vid *WrappedTx) DeltaString() string {
	ret := lines.New().Add("== delta of %s", vid.IDShort())
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret.Append(v.StateDelta.Lines())
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
		util.Assertf(v.IsSolid(), "not solid inputs of %s", v.Tx.IDShort())

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

// DeltaBranchVID returns txID of the state to which the delta will be applied
// For the branch transaction it is the txID of itself
func (vid *WrappedTx) DeltaBranchVID() (ret *WrappedTx) {
	if vid.IsBranchTransaction() {
		ret = vid
		return
	}
	ret = vid.BaselineBranchVID()
	return
}

func (vid *WrappedTx) BaselineBranchVID() (ret *WrappedTx) {
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.StateDelta.baselineVID
	}})
	return
}

// GetUTXOStateDelta returns a pointer, not to be mutated, must be cloned first! Never nil
// For branch it returns empty delta with the branch as baseline
func (vid *WrappedTx) GetUTXOStateDelta() *UTXOStateDelta {
	if vid.IsBranchTransaction() {
		return NewUTXOStateDelta(vid.DeltaBranchVID())
	}
	return vid.GetBaselineDelta()
}

func (vid *WrappedTx) GetBaselineDelta() (ret *UTXOStateDelta) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = &v.StateDelta
		},
		VirtualTx: func(v *VirtualTransaction) {
			ret = NewUTXOStateDelta(nil)
		},
		Orphaned: PanicOrphaned,
	})
	return
}

func PanicOrphaned() {
	util.Panicf("orphaned transaction should not be accessed")
}

func (vid *WrappedTx) LedgerCoverage(getStateStore func() general.StateStore) uint64 {
	baselineVID := vid.BaselineBranchVID()
	if baselineVID == nil {
		return vid.GetUTXOStateDelta().Coverage()
	}
	deltaCoverage := uint64(0)
	if getStateStore != nil {
		bd, ok := multistate.FetchBranchData(getStateStore(), *baselineVID.ID())
		util.Assertf(ok, "can't fetch branch data for %s", baselineVID.IDShort())
		deltaCoverage = bd.Coverage
	}

	return deltaCoverage + vid.GetUTXOStateDelta().Coverage()
}

func (vid *WrappedTx) MustConsistentDelta(ut *UTXOTangle) {
	err := util.CatchPanicOrError(func() error {
		vid.GetBaselineDelta().MustCheckConsistency(ut.MustGetStateReader)
		return nil
	})
	if err != nil {
		SaveGraphPastCone(vid, "inconsistent_delta")
		panic(err)
	}
}

func (vid *WrappedTx) propagateNewForkToFutureCone(f Fork, ut *UTXOTangle) {
	vid.descendants.ForEach(func(descendant *WrappedTx) bool {
		descendant._propagateNewForkToFutureCone(f, ut, set.New[*WrappedTx]())
		return true
	})
}

func (vid *WrappedTx) _propagateNewForkToFutureCone(f Fork, ut *UTXOTangle, visited set.Set[*WrappedTx]) {
	if visited.Contains(vid) {
		return
	}
	visited.Insert(vid)
	success := vid.addFork(f)
	util.Assertf(success, "unexpected conflict while propagating new fork")

	vid.descendants.ForEach(func(descendant *WrappedTx) bool {
		descendant._propagateNewForkToFutureCone(f, ut, visited)
		return true
	})
}

func (vid *WrappedTx) addFork(f Fork) bool {
	ret := true
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.addFork(f)
	}})
	return ret
}

// addConsumer must be called from globally locked utangle environment
func (vid *WrappedTx) addConsumer(consumer *WrappedTx, outputIndex byte, ut *UTXOTangle) {
	if vid.descendants == nil {
		vid.descendants = set.New[*WrappedTx]()
	}
	if vid.consumers == nil {
		vid.consumers = make(map[byte]uint16)
	}
	vid.descendants.Insert(consumer)

	sn := vid.consumers[outputIndex]
	vid.consumers[outputIndex] = sn + 1

	if sn == 1 {
		// it is the second consumer, i.e. new double spend. Propagate it to the future cone
		// for subsequent consumers no need to propagate
		f := NewFork(WrappedOutput{VID: vid, Index: outputIndex}, sn)
		vid.propagateNewForkToFutureCone(f, ut)
	}
}

func (vid *WrappedTx) addEndorser(endorser *WrappedTx) {
	if vid.descendants == nil {
		vid.descendants = set.New[*WrappedTx]()
	}
	vid.descendants.Insert(endorser)
}

func (vid *WrappedTx) BaselineBranch() (ret *WrappedTx) {
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.BaselineBranch()
	}})
	return
}

type _mutationData struct {
	outputMutations     map[core.OutputID]*core.Output
	addTxMutations      []*core.TransactionID
	visited             set.Set[*WrappedTx]
	baselineStateReader general.StateReader
}

func (vid *WrappedTx) _collectMutationData(md *_mutationData) (conflict WrappedOutput) {
	if md.visited.Contains(vid) {
		return
	}
	md.visited.Insert(vid)
	if md.baselineStateReader.KnowsCommittedTransaction(vid.ID()) {
		return
	}

	md.addTxMutations = append(md.addTxMutations, vid.ID())

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
				inp._collectMutationData(md)

				inputID := v.Tx.MustInputAt(i)
				if o, produced := md.outputMutations[inputID]; produced {
					util.Assertf(o != nil, "unexpected double DEL mutation at %s", inputID.Short())
					delete(md.outputMutations, inputID)
				} else {
					if md.baselineStateReader.HasUTXO(&inputID) {
						md.outputMutations[inputID] = nil
					} else {
						// output does not exist in the state
						conflict = WrappedOutput{VID: inp, Index: v.Tx.MustOutputIndexOfTheInput(i)}
						return false
					}
				}
				return true
			})
			v.Tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
				md.outputMutations[*oid] = o
				return true
			})
		},
		Orphaned: PanicOrphaned,
	})
	return
}

func (vid *WrappedTx) getBranchMutations(ut *UTXOTangle) (*multistate.Mutations, WrappedOutput) {
	util.Assertf(vid.IsBranchTransaction(), "%s not a branch transaction", vid.IDShort())

	baselineBranchVID := vid.BaselineBranch()
	util.Assertf(baselineBranchVID != nil, "can't get baseline branch for %s", vid.IDShort())

	md := &_mutationData{
		outputMutations:     make(map[core.OutputID]*core.Output),
		addTxMutations:      make([]*core.TransactionID, 0),
		visited:             set.New[*WrappedTx](),
		baselineStateReader: ut.MustGetStateReader(baselineBranchVID.ID(), 1000),
	}
	if conflict := vid._collectMutationData(md); conflict.VID != nil {
		return nil, conflict
	}
	ret := multistate.NewMutations()
	for oid, o := range md.outputMutations {
		if o != nil {
			ret.InsertAddOutputMutation(oid, o)
		} else {
			ret.InsertDelOutputMutation(oid)
		}
	}
	slot := vid.TimeSlot()
	for _, txid := range md.addTxMutations {
		ret.InsertAddTxMutation(*txid, slot)
	}
	return ret.Sort(), WrappedOutput{}
}
