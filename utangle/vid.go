package utangle

import (
	"bytes"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	// WrappedTx value of *WrappedTx is used as transaction identity on the UTXO tangle
	// Behind this identity can be wrapped usual vertex, virtual or orphaned transactions
	WrappedTx struct {
		mutex sync.RWMutex // protects _genericWrapper
		_genericWrapper
		// future cone references. Protected by global utangle lock
		// numConsumers contains number of consumers for outputs
		consumers map[byte][]*WrappedTx
		// descendants is a list of consumers and endorsers, repeated once
		endorsers []*WrappedTx
	}

	WrappedOutput struct {
		VID   *WrappedTx
		Index byte
	}

	// _genericWrapper generic types of vertex hiding behind WrappedTx identity
	_genericWrapper interface {
		_id() *core.TransactionID
		_time() time.Time
		_outputAt(idx byte) (*core.Output, error)
		_hasOutputAt(idx byte) (bool, bool)
	}

	_vertex struct {
		*Vertex
		whenWrapped time.Time
	}

	_virtualTx struct {
		*VirtualTransaction
	}

	_deletedTx struct {
		core.TransactionID
	}

	UnwrapOptions struct {
		Vertex    func(v *Vertex)
		VirtualTx func(v *VirtualTransaction)
		Deleted   func()
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

func (v _deletedTx) _id() *core.TransactionID {
	return &v.TransactionID
}

func (v _deletedTx) _time() time.Time {
	return time.Time{}
}

func (v _deletedTx) _outputAt(_ byte) (*core.Output, error) {
	panic("orphaned vertex should not be accessed")
}

func (v _deletedTx) _hasOutputAt(idx byte) (bool, bool) {
	panic("orphaned vertex should not be accessed")
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
	return vid.ID().StringShort()
}

func (vid *WrappedTx) LazyIDShort() func() any {
	return func() any {
		return vid.IDShort()
	}
}

func (vid *WrappedTx) IDVeryShort() string {
	return vid.ID().StringVeryShort()
}

func (vid *WrappedTx) IsBranchTransaction() bool {
	return vid.ID().BranchFlagON()
}

func (vid *WrappedTx) IsSequencerMilestone() bool {
	return vid.ID().SequencerFlagON()
}

func (vid *WrappedTx) Timestamp() core.LogicalTime {
	return vid.ID().Timestamp()
}

func (vid *WrappedTx) TimeSlot() core.TimeSlot {
	return vid._genericWrapper._id().TimeSlot()
}

func (vid *WrappedTx) MarkDeleted() {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	switch v := vid._genericWrapper.(type) {
	case _vertex:
		vid._put(_deletedTx{TransactionID: *v.Tx.ID()})
	case _virtualTx:
		vid._put(_deletedTx{TransactionID: v.txid})
	case _deletedTx:
		PanicDeleted()
	}
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

// BaseStemOutput returns wrapped stem output for the branch state or nil if unavailable
func (vid *WrappedTx) BaseStemOutput(ut *UTXOTangle) *WrappedOutput {
	var branchTxID *core.TransactionID
	if vid.IsBranchTransaction() {
		branchTxID = vid.ID()
	} else {
		baselineVID := vid.BaselineBranch()
		if baselineVID == nil {
			return nil
		}
		branchTxID = baselineVID.ID()
	}
	oid, ok := multistate.FetchStemOutputID(ut.stateStore, *branchTxID)
	if !ok {
		return nil
	}
	ret, found, invalid := ut.GetWrappedOutput(&oid)
	util.Assertf(found && !invalid, "found & !invalid")

	return &ret
}

func (vid *WrappedTx) UnwrapVertexForReadOnly() (ret *Vertex, retOk bool) {
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

func (vid *WrappedTx) isVirtualTx() (ret bool) {
	vid.Unwrap(UnwrapOptions{VirtualTx: func(_ *VirtualTransaction) {
		ret = true
	}})
	return
}

func (vid *WrappedTx) IsDeleted() bool {
	ret := false
	vid.Unwrap(UnwrapOptions{
		Deleted: func() {
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

func (vid *WrappedTx) Unwrap(opt UnwrapOptions) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	switch v := vid._genericWrapper.(type) {
	case _vertex:
		if opt.Vertex != nil {
			opt.Vertex(v.Vertex)
		}
	case _virtualTx:
		if opt.VirtualTx != nil {
			opt.VirtualTx(v.VirtualTransaction)
		}
	case _deletedTx:
		if opt.Deleted != nil {
			opt.Deleted()
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
				ret.Append(v.outputs[i].Lines("     "))
			}
		},
		Deleted: func() {
			ret.Add("== orphaned vertex")
		},
	})
	return ret
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
	return o.DecodeID().StringShort()
}

func (o *WrappedOutput) Unwrap() (ret *core.OutputWithID, err error) {
	return o.VID.OutputWithIDAt(o.Index)
}

func (o *WrappedOutput) Amount() uint64 {
	out, err := o.VID.OutputAt(o.Index)
	util.AssertNoError(err)
	return out.Amount()
}

func (o *WrappedOutput) Timestamp() core.LogicalTime {
	return o.VID.Timestamp()
}

func (o *WrappedOutput) TimeSlot() core.TimeSlot {
	return o.VID.TimeSlot()
}

func (vid *WrappedTx) ConvertToVirtualTx() {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	switch v := vid._genericWrapper.(type) {
	case _vertex:
		vid._put(_virtualTx{VirtualTransaction: v.convertToVirtualTx()})
	case _deletedTx:
		PanicDeleted()
	}
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

func PanicDeleted() {
	util.Panicf("deleted transaction should not be accessed")
}

// attachAsConsumer must be called from globally locked utangle environment.
// Double-links conflict. Propagates new conflict
func (vid *WrappedTx) attachAsConsumer(outputIndex byte, consumer *WrappedTx) *WrappedOutput {
	if vid.consumers == nil {
		vid.consumers = make(map[byte][]*WrappedTx)
	}
	descendants := vid.consumers[outputIndex]
	if len(descendants) >= int(ForkSNReserved) {
		// maximum 255 conflicts per output. Sorry
		return &WrappedOutput{
			VID:   vid,
			Index: outputIndex,
		}
	}
	switch sn := byte(len(descendants)); sn {
	case 0:
		descendants = make([]*WrappedTx, 0, 2)
		// only double-spends need marking the consumer with fork
	case 1:
		// double-spend requires propagation of the new fork
		f := newFork(WrappedOutput{VID: vid, Index: outputIndex}, 0)
		descendants[0].propagateNewForkToFutureCone(f, set.New[*WrappedTx]())
		// mark the consumer with fork
		consumer.addFork(newFork(WrappedOutput{VID: vid, Index: outputIndex}, 1))
	default:
		// mark the consumer with fork
		consumer.addFork(newFork(WrappedOutput{VID: vid, Index: outputIndex}, sn))
	}
	vid.consumers[outputIndex] = util.AppendUnique(descendants, consumer)
	return nil
}

func (vid *WrappedTx) propagateNewForkToFutureCone(f Fork, visited set.Set[*WrappedTx]) {
	if vid.IsDeleted() {
		return
	}
	if visited.Contains(vid) {
		return
	}
	visited.Insert(vid)

	for _, consumers := range vid.consumers {
		for _, descendant := range consumers {
			descendant.propagateNewForkToFutureCone(f, visited)
		}
	}
	for _, vidEndorser := range vid.endorsers {
		vidEndorser.propagateNewForkToFutureCone(f, visited)
	}
	ok := vid.addFork(f)
	util.Assertf(ok, "unexpected conflict while propagating new fork")
}

func (vid *WrappedTx) addFork(f Fork) bool {
	ret := true
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.pastTrack.forks.insert(f)
		},
	})
	return ret
}

func (vid *WrappedTx) attachAsEndorser(endorser *WrappedTx) {
	if len(vid.endorsers) == 0 {
		vid.endorsers = make([]*WrappedTx, 0)
	}
	vid.endorsers = util.AppendUnique(vid.endorsers, endorser)
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
	baselineStateReader global.StateReader
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

	// FIXME revisit mutations

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
				// recursively collect from inputs
				inp._collectMutationData(md)

				inputID := v.Tx.MustInputAt(i)
				if _, produced := md.outputMutations[inputID]; produced {
					// disable assert: it may be deleted repeatedly along endorsement lines
					// util.Assertf(o != nil, "unexpected double DEL mutation at %s", inputID.StringShort())
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
			v.forEachEndorsement(func(i byte, vidEndorsed *WrappedTx) bool {
				// recursively collect from endorsements
				vidEndorsed._collectMutationData(md)
				return true
			})
			v.Tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
				_, already := md.outputMutations[*oid]
				util.Assertf(!already, "repeating ADD mutation %s", oid.StringShort())
				md.outputMutations[*oid] = o
				return true
			})
		},
		Deleted: PanicDeleted,
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

func (vid *WrappedTx) _collectBaselineOutputs(baselineStateReader global.StateReader, visited set.Set[*WrappedTx], baselineOutputs set.Set[WrappedOutput]) {
	if visited.Contains(vid) {
		return
	}
	visited.Insert(vid)

	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
			wInp := WrappedOutput{
				VID:   inp,
				Index: v.Tx.MustOutputIndexOfTheInput(i),
			}
			if baselineStateReader.HasUTXO(wInp.DecodeID()) {
				baselineOutputs.Insert(wInp)
			} else {
				inp._collectBaselineOutputs(baselineStateReader, visited, baselineOutputs)
			}
			return true
		})
		v.forEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
			vEnd._collectBaselineOutputs(baselineStateReader, visited, baselineOutputs)
			return true
		})
	}})
	return
}

func (vid *WrappedTx) CoverageDelta(ut *UTXOTangle) (*core.TransactionID, uint64) {
	if ut == nil {
		// TODO temporary
		return nil, 0
	}
	baselineBranchVID := vid.BaselineBranch()
	if baselineBranchVID == nil {
		return nil, 0
	}

	baselineTxID := baselineBranchVID.ID()
	baselineOutputs := set.New[WrappedOutput]()
	vid._collectBaselineOutputs(ut.MustGetStateReader(baselineTxID), set.New[*WrappedTx](), baselineOutputs)

	ret := uint64(0)
	bd, found := multistate.FetchBranchData(ut.stateStore, *baselineTxID)
	util.Assertf(found, "can't found root record for %s", baselineTxID.StringShort())
	coverageCap := bd.Stem.Output.MustStemLock().Supply

	baselineOutputs.ForEach(func(o WrappedOutput) bool {
		ret += o.Amount()
		return true
	})
	if ret > coverageCap {
		baselineOutputsLines := baselineOutputs.Lines(func(key WrappedOutput) string {
			return key.IDShort()
		})
		SaveGraphPastCone(vid, "failed_coverage")
		util.Panicf("CoverageDelta inconsistency: result for vertex %s = %s -> exceeds current total supply %s. Outputs summed up:\n%s",
			vid.IDShort(), util.GoThousands(ret), util.GoThousands(coverageCap), baselineOutputsLines.String(),
		)
	}
	return baselineTxID, ret
}

func (vid *WrappedTx) LedgerCoverage(ut *UTXOTangle) uint64 {
	var deltaCoverage uint64
	var branchTxID *core.TransactionID

	branchTxID, deltaCoverage = vid.CoverageDelta(ut)
	if vid.IsBranchTransaction() || branchTxID == nil {
		return deltaCoverage
	}

	bd, ok := ut.FetchBranchData(branchTxID)
	util.Assertf(ok, "can't fetch branch data for %s", func() any { return branchTxID.StringShort() })

	return deltaCoverage + bd.LedgerCoverage.Sum()
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
		Deleted: PanicDeleted,
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

	ret.Add("==== BEGIN forks of %s", vid.IDShort())
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
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
		v.forEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
			ret.Add("  ENDORSEMENT %d : %s ", i, vEnd.IDShort())
			vEnd.Unwrap(UnwrapOptions{Vertex: func(vEnd *Vertex) {
				ret.Append(vEnd.pastTrack.Lines("     "))
			}})
			return true
		})
		ret.Add("  MERGED:")
		ret.Append(v.pastTrack.Lines("      "))
	}})
	ret.Add("==== END forks of %s", vid.IDShort())
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
			v.forEachInputDependency(func(i byte, vidInput *WrappedTx) bool {
				if o.VID == vidInput {
					consumed = o.Index == v.Tx.MustOutputIndexOfTheInput(i)
				} else {
					consumed = o._isConsumedInThePastConeOf(vidInput, visited)
				}
				return !consumed
			})
			if !consumed {
				v.forEachEndorsement(func(_ byte, vidEndorsed *WrappedTx) bool {
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
