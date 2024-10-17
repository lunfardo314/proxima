package vertex

import (
	"bytes"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

// ErrDeletedVertexAccessed exception is raised by PanicAccessDeleted handler of RUnwrap vertex so that could be caught if necessary
var ErrDeletedVertexAccessed = errors.New("deleted vertex should not be accessed")

func (v _vertex) _outputAt(idx byte) (*ledger.Output, error) {
	return v.Tx.ProducedOutputAt(idx)
}

func (v _virtualTx) _outputAt(idx byte) (*ledger.Output, error) {
	if o, available := v.OutputAt(idx); available {
		return o, nil
	}
	return nil, nil
}

func _newVID(g _genericVertex, txid ledger.TransactionID, seqID *ledger.ChainID) *WrappedTx {
	ret := &WrappedTx{
		ID:             txid,
		_genericVertex: g,
		numReferences:  1, // we always start with 1 reference, which is reference by the MemDAG itself. 0 references means it is deleted
		dontPruneUntil: time.Now().Add(vertexTTLSlots * ledger.SlotDuration()),
	}
	ret.SequencerID.Store(seqID)
	ret.onPoke.Store(func() {})
	return ret
}

func (vid *WrappedTx) _put(g _genericVertex) {
	vid._genericVertex = g
}

func (vid *WrappedTx) FlagsNoLock() Flags {
	return vid.flags
}

func (vid *WrappedTx) SetFlagsUpNoLock(f Flags) {
	vid.flags = vid.flags | f
}

func (vid *WrappedTx) FlagsUp(f Flags) bool {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.flags&f == f
}

func (vid *WrappedTx) FlagsUpNoLock(f Flags) bool {
	return vid.flags&f == f
}

func (vid *WrappedTx) ConvertVirtualTxToVertexNoLock(v *Vertex) {
	util.Assertf(vid.ID == *v.Tx.ID(), "ConvertVirtualTxToVertexNoLock: txid-s do not match in: %s", vid.ID.StringShort)
	_, isVirtualTx := vid._genericVertex.(_virtualTx)
	util.Assertf(isVirtualTx, "ConvertVirtualTxToVertexNoLock: virtual tx target expected %s", vid.ID.StringShort)
	vid._put(_vertex{Vertex: v})
	if v.Tx.IsSequencerMilestone() {
		vid.SequencerID.Store(util.Ref(v.Tx.SequencerTransactionData().SequencerID))
	}
}

// ConvertVertexToVirtualTx detaches past cone and leaves only a collection of produced outputs
func (vid *WrappedTx) ConvertVertexToVirtualTx() {
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		vid._put(_virtualTx{VirtualTxFromTx(v.Tx)})
		v.UnReferenceDependencies()
	}})
}

func (vid *WrappedTx) GetTxStatus() Status {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.GetTxStatusNoLock()
}

func (vid *WrappedTx) GetTxStatusNoLock() Status {
	if !vid.flags.FlagsUp(FlagVertexDefined) {
		util.Assertf(vid.err == nil, "vid.err == nil")
		return Undefined
	}
	if vid.err != nil {
		return Bad
	}
	if !vid.flags.FlagsUp(FlagVertexIgnoreAbsenceOfPastCone) {
		util.Assertf(vid.IsBranchTransaction() || vid.pastCone != nil, "vid.IsBranchTransaction() || vid.pastCone!= nil")
	}
	return Good
}

// GetTxStatusExt returns status and past cone. If vertex is 'good', past cone is not nil
func (vid *WrappedTx) GetTxStatusExt() (Status, *PastConeBase) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.GetTxStatusNoLock(), vid.pastCone
}

// SetTxStatusGood sets 'good' status and past cone
func (vid *WrappedTx) SetTxStatusGood(pastCone *PastConeBase) {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	util.Assertf(vid.GetTxStatusNoLock() != Bad, "vid.GetTxStatusNoLock() != Bad (%s)", vid.StringNoLock)

	vid.flags.SetFlagsUp(FlagVertexDefined)
	if pastCone == nil {
		vid.flags.SetFlagsUp(FlagVertexIgnoreAbsenceOfPastCone)
	} else {
		vid.pastCone = pastCone
	}
}

func (vid *WrappedTx) SetSequencerAttachmentFinished() {
	util.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")

	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	util.Assertf(vid.flags.FlagsUp(FlagVertexTxAttachmentStarted), "vid.flags.FlagsUp(FlagVertexTxAttachmentStarted)")
	vid.flags.SetFlagsUp(FlagVertexTxAttachmentFinished)
	vid.dontPruneUntil = time.Now().Add(vertexTTLSlots * ledger.L().ID.SlotDuration())
}

func (vid *WrappedTx) SetTxStatusBad(reason error) {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	vid.SetTxStatusBadNoLock(reason)
	vid.SetFlagsUpNoLock(FlagVertexTxAttachmentFinished)
}

func (vid *WrappedTx) SetTxStatusBadNoLock(reason error) {
	util.Assertf(reason != nil, "SetTxStatusBadNoLock: reason must be not nil")
	util.Assertf(vid.GetTxStatusNoLock() != Good || errors.Is(reason, global.ErrInterrupted),
		"vid.GetTxStatusNoLock() != Good. SetTxStatusBadNoLock err = %v", reason)
	vid.flags.SetFlagsUp(FlagVertexDefined)
	vid.err = reason
}

func (vid *WrappedTx) GetError() error {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.err
}

func (vid *WrappedTx) GetErrorNoLock() error {
	return vid.err
}

// IsBadOrDeleted non-deterministic
func (vid *WrappedTx) IsBadOrDeleted() bool {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.GetTxStatusNoLock() == Bad || vid.numReferences == 0
}

func (vid *WrappedTx) OnPoke(fun func()) {
	if fun == nil {
		vid.onPoke.Store(func() {})
	} else {
		vid.onPoke.Store(fun)
	}
}

func (vid *WrappedTx) Poke() {
	vid.onPoke.Load().(func())()
}

// WrapTxID creates VID with virtualTx which only contains txid.
// Also sets solidification deadline, after which IsPullDeadlineDue will start returning true
// The pull deadline will be dropped after transaction will become available and virtualTx will be converted
// to full vertex
func WrapTxID(txid ledger.TransactionID) *WrappedTx {
	return _newVID(_virtualTx{newVirtualTx()}, txid, nil)
}

func (vid *WrappedTx) ShortString() string {
	var mode, status, reason string
	flagsStr := ""
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			mode = "vertex"
			flagsStr = fmt.Sprintf(", %08b", vid.flags)
			status = vid.GetTxStatusNoLock().String()
			if vid.err != nil {
				reason = fmt.Sprintf(" err: '%v'", vid.err)
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			mode = "virtualTx"
			status = vid.GetTxStatusNoLock().String()
			if vid.err != nil {
				reason = fmt.Sprintf(" err: '%v'", vid.err)
			}
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return fmt.Sprintf("%22s %10s (%s%s) %s", vid.IDShortString(), mode, status, flagsStr, reason)
}

func (vid *WrappedTx) IDShortString() string {
	return vid.ID.StringShort()
}

func (vid *WrappedTx) IDVeryShort() string {
	return vid.ID.StringVeryShort()
}

func (vid *WrappedTx) IsBranchTransaction() bool {
	return vid.ID.IsBranchTransaction()
}

func (vid *WrappedTx) IsSequencerMilestone() bool {
	return vid.ID.IsSequencerMilestone()
}

func (vid *WrappedTx) Timestamp() ledger.Time {
	return vid.ID.Timestamp()
}

func (vid *WrappedTx) Before(vid1 *WrappedTx) bool {
	return vid.Timestamp().Before(vid1.Timestamp())
}

func (vid *WrappedTx) Slot() ledger.Slot {
	return vid.ID.Slot()
}

func (vid *WrappedTx) OutputWithIDAt(idx byte) (ledger.OutputWithID, error) {
	ret, err := vid.OutputAt(idx)
	if err != nil || ret == nil {
		return ledger.OutputWithID{}, err
	}
	return ledger.OutputWithID{
		ID:     ledger.NewOutputID(&vid.ID, idx),
		Output: ret,
	}, nil
}

func (vid *WrappedTx) MustOutputWithIDAt(idx byte) (ret ledger.OutputWithID) {
	var err error
	ret, err = vid.OutputWithIDAt(idx)
	util.AssertNoError(err)
	return
}

// OutputAt return output at index, if available.
// err != nil indicates wrong index
// nil, nil means output not available, but no error (orphaned)
func (vid *WrappedTx) OutputAt(idx byte) (*ledger.Output, error) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid._outputAt(idx)
}

func (vid *WrappedTx) MustOutputAt(idx byte) *ledger.Output {
	ret, err := vid.OutputAt(idx)
	util.AssertNoError(err)
	return ret
}

func (vid *WrappedTx) SequencerIDStringVeryShort() string {
	cid := vid.SequencerID.Load()
	if cid == nil {
		return "/$??"
	}
	return cid.StringVeryShort()
}

func (vid *WrappedTx) MustSequencerIDAndStemID() (seqID ledger.ChainID, stemID ledger.OutputID) {
	util.Assertf(vid.IsBranchTransaction(), "vid.IsBranchTransaction()")
	p := vid.SequencerID.Load()
	util.Assertf(p != nil, "sequencerID is must be not nil")
	seqID = *p
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			stemID = vid.OutputID(v.Tx.SequencerTransactionData().StemOutputIndex)
		},
		VirtualTx: func(v *VirtualTransaction) {
			util.Assertf(v.sequencerOutputIndices != nil, "v.sequencerOutputs != nil")
			stemID = vid.OutputID(v.sequencerOutputIndices[1])
		},
	})
	return
}

func (vid *WrappedTx) SequencerWrappedOutput() (ret WrappedOutput) {
	util.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")

	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if seqData := v.Tx.SequencerTransactionData(); seqData != nil {
				ret = WrappedOutput{
					VID:   vid,
					Index: v.Tx.SequencerTransactionData().SequencerOutputIndex,
				}
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			if v.sequencerOutputIndices != nil {
				ret = WrappedOutput{
					VID:   vid,
					Index: v.sequencerOutputIndices[0],
				}
			}
		},
	})
	return
}

func (vid *WrappedTx) FindChainOutput(chainID *ledger.ChainID) (ret *ledger.OutputWithID) {
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.Tx.FindChainOutput(*chainID)
		},
		VirtualTx: func(v *VirtualTransaction) {
			ret = v.findChainOutput(&vid.ID, chainID)
		},
	})
	return
}

func (vid *WrappedTx) StemWrappedOutput() (ret WrappedOutput) {
	util.Assertf(vid.IsBranchTransaction(), "vid.IsBranchTransaction()")

	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if seqData := v.Tx.SequencerTransactionData(); seqData != nil {
				ret = WrappedOutput{
					VID:   vid,
					Index: v.Tx.SequencerTransactionData().StemOutputIndex,
				}
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			if v.sequencerOutputIndices != nil {
				ret = WrappedOutput{
					VID:   vid,
					Index: v.sequencerOutputIndices[1],
				}
			}
		},
	})
	return
}

func (vid *WrappedTx) IsVirtualTx() (ret bool) {
	vid.RUnwrap(UnwrapOptions{VirtualTx: func(_ *VirtualTransaction) {
		ret = true
	}})
	return
}

func (vid *WrappedTx) _ofKindString() (ret string) {
	vid._unwrap(UnwrapOptions{
		Vertex:    func(_ *Vertex) { ret = "full vertex" },
		VirtualTx: func(_ *VirtualTransaction) { ret = "virtualTx" },
		Deleted:   func() { ret = "deleted" },
	})
	return
}

func (vid *WrappedTx) OutputID(idx byte) (ret ledger.OutputID) {
	ret = ledger.NewOutputID(&vid.ID, idx)
	return
}

func (vid *WrappedTx) Unwrap(opt UnwrapOptions) {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	vid._unwrap(opt)
}

func (vid *WrappedTx) RUnwrap(opt UnwrapOptions) {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	vid._unwrap(opt)
}

func (vid *WrappedTx) _unwrap(opt UnwrapOptions) {
	switch v := vid._genericVertex.(type) {
	case _vertex:
		if opt.Vertex != nil {
			opt.Vertex(v.Vertex)
		}
	case _virtualTx:
		if opt.VirtualTx != nil {
			opt.VirtualTx(v.VirtualTransaction)
		}
	default:
		if vid.numReferences == 0 && opt.Deleted != nil {
			opt.Deleted()
		}
	}
}

func (vid *WrappedTx) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret.Add("== vertex %s", vid.IDShortString())
			ret.Append(v.Lines(prefix...))
		},
		VirtualTx: func(v *VirtualTransaction) {
			ret.Add("== virtual tx %s", vid.IDShortString())
			if v.sequencerOutputIndices == nil {
				ret.Add("seq output indices: <nil>")
			} else {
				ret.Add("seq output indices: (%d, %d)", (v.sequencerOutputIndices)[0], (v.sequencerOutputIndices)[1])
			}
			idxs := util.KeysSorted(v.outputs, func(k1, k2 byte) bool {
				return k1 < k2
			})
			for _, i := range idxs {
				ret.Add("    #%d :", i)
				ret.Append(v.outputs[i].Lines("     "))
			}
		},
		Deleted: func() {
			ret.Add("== deleted vertex")
		},
	})
	return ret
}

func (vid *WrappedTx) LinesNoLock(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("ID: %s", vid.ID.StringShort()).
		Add("Kind: %s", vid._ofKindString()).
		Add("Status: %s", vid.GetTxStatusNoLock().String()).
		Add("Flags: %s", vid.flags.String()).
		Add("Err: %v", vid.err).
		Add("Refs: %d", vid.numReferences)
	if seqID := vid.SequencerID.Load(); seqID == nil {
		ret.Add("Seq ID: <nil>")
	} else {
		ret.Add("Seq ID: %s", seqID.StringShort())
	}
	switch v := vid._genericVertex.(type) {
	case _vertex:
		ret.Add("---- transaction ----\n" + v.Tx.LinesShort(prefix...).String())
	case _virtualTx:
		if v.needsPull {
			ret.Add("Pull: number of pulls: %d, next pull in %v", v.timesPulled, time.Until(v.nextPull))
		} else {
			ret.Add("Pull: not needed")
		}
	}
	return ret
}

func (vid *WrappedTx) StringNoLock() string {
	return vid.LinesNoLock("   ").String()
}

func (vid *WrappedTx) NumInputs() int {
	ret := 0
	vid.RUnwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.Tx.NumInputs()
	}})
	return ret
}

func (vid *WrappedTx) NumProducedOutputs() int {
	ret := 0
	vid.RUnwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = v.Tx.NumProducedOutputs()
	}})
	return ret
}

func (vid *WrappedTx) convertToVirtualTxNoLock() {
	util.Assertf(vid.numReferences > 0, "convertToVirtualTxNoLock: access deleted tx in %s", vid.IDShortString)
	v, isVertex := vid._genericVertex.(_vertex)
	util.Assertf(isVertex, "convertToVirtualTxNoLock: must be full vertex %s", vid.IDShortString)

	vid._put(_virtualTx{VirtualTransaction: v.toVirtualTx()})
}

func (vid *WrappedTx) PanicAccessDeleted() {
	fmt.Printf(">>>>>>>>>>>>>>>>> PanicAccessDeleted:\n%s\n<<<<<<<<<<<<<<<<<<<<<<<\n", string(debug.Stack()))
	util.Panicf("%w: %s", ErrDeletedVertexAccessed, vid.ID.StringShort())
}

// BaselineBranch baseline branch of the vertex
// Will return nil for virtual transaction
func (vid *WrappedTx) BaselineBranch() (baselineBranch *WrappedTx) {
	if vid.ID.IsBranchTransaction() {
		return vid
	}
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			baselineBranch = v.BaselineBranch
		},
		VirtualTx: func(v *VirtualTransaction) {
			baselineBranch = v.baselineBranch
		},
	})
	return
}

func (vid *WrappedTx) EnsureOutputWithID(o *ledger.OutputWithID) (err error) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			idx := o.ID.Index()
			if idx >= byte(v.Tx.NumProducedOutputs()) {
				err = fmt.Errorf("EnsureOutputWithID: wrong output index in %s", util.Ref(v.Tx.OutputID(idx)).StringShort())
				return
			}
			if !bytes.Equal(o.Output.Bytes(), v.Tx.MustProducedOutputAt(idx).Bytes()) {
				err = fmt.Errorf("EnsureOutputWithID: inconsistent output data in %s", util.Ref(v.Tx.OutputID(idx)).StringShort())
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			err = v.addOutput(o.ID.Index(), o.Output)
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return err
}

// AttachConsumer stores consumer of the vid[outputIndex] consumed output.
// Function checkConflicts checks if new consumer conflicts with already existing ones
func (vid *WrappedTx) AttachConsumer(outputIndex byte, consumer *WrappedTx, checkConflicts func(existingConsumers set.Set[*WrappedTx]) (conflict *WrappedTx)) (conflict *WrappedTx) {
	vid.mutexDescendants.Lock()
	defer vid.mutexDescendants.Unlock()

	if vid.consumed == nil {
		vid.consumed = make(map[byte]set.Set[*WrappedTx])
	}
	outputConsumers := vid.consumed[outputIndex]
	if outputConsumers == nil {
		outputConsumers = set.New(consumer)
	} else {
		outputConsumers.Insert(consumer)
	}
	vid.consumed[outputIndex] = outputConsumers
	return checkConflicts(outputConsumers)
}

func (vid *WrappedTx) NotConsumedOutputIndices(allConsumers set.Set[*WrappedTx]) []byte {
	vid.mutexDescendants.Lock()
	defer vid.mutexDescendants.Unlock()

	nOutputs := 0
	vid.RUnwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		nOutputs = v.Tx.NumProducedOutputs()
	}})

	ret := make([]byte, 0, nOutputs)

	for i := 0; i < nOutputs; i++ {
		if set.DoNotIntersect(vid.consumed[byte(i)], allConsumers) {
			ret = append(ret, byte(i))
		}
	}
	return ret
}

func (vid *WrappedTx) GetLedgerCoverageNoLock() *uint64 {
	return vid.coverage
}

func (vid *WrappedTx) GetLedgerCoverageP() *uint64 {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.coverage
}

func (vid *WrappedTx) GetLedgerCoverage() uint64 {
	ret := vid.GetLedgerCoverageP()
	if ret == nil {
		return 0
	}
	return *ret
}

func (vid *WrappedTx) GetLedgerCoverageString() string {
	return util.Th(vid.GetLedgerCoverage())
}

func (vid *WrappedTx) SetLedgerCoverage(coverage uint64) {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	vid.coverage = new(uint64)
	*vid.coverage = coverage
}

// NumConsumers returns:
// - number of consumed outputs
// - number of conflict sets
func (vid *WrappedTx) NumConsumers() (int, int) {
	vid.mutexDescendants.RLock()
	defer vid.mutexDescendants.RUnlock()

	retConsumed := len(vid.consumed)
	retCS := 0
	for _, ds := range vid.consumed {
		if len(ds) > 1 {
			retCS++
		}
	}
	return retConsumed, retCS
}

func (vid *WrappedTx) ConsumersOf(outIdx byte) set.Set[*WrappedTx] {
	vid.mutexDescendants.RLock()
	defer vid.mutexDescendants.RUnlock()

	return vid.consumed[outIdx].Clone()
}

func (vid *WrappedTx) String() (ret string) {
	consumed, doubleSpent := vid.NumConsumers()
	reason := vid.GetError()
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			cov := uint64(0)
			if vid.coverage != nil {
				cov = *vid.coverage
			}
			t := "vertex (" + vid.GetTxStatusNoLock().String() + ")"
			ret = fmt.Sprintf("%20s %s :: in: %d, out: %d, consumed: %d, conflicts: %d, ref: %d, Flags: %08b, err: '%v', cov: %s",
				t,
				vid.ID.StringShort(),
				v.Tx.NumInputs(),
				v.Tx.NumProducedOutputs(),
				consumed,
				doubleSpent,
				vid.numReferences,
				vid.flags,
				reason,
				util.Th(cov),
			)
		},
		VirtualTx: func(v *VirtualTransaction) {
			t := "virtualTx (" + vid.GetTxStatus().String() + ")"

			v.mutex.RLock()
			defer v.mutex.RUnlock()

			ret = fmt.Sprintf("%20s %s:: out: %d, consumed: %d, conflicts: %d, flags: %08b, err: %v",
				t,
				vid.ID.StringShort(),
				len(v.outputs),
				consumed,
				doubleSpent,
				vid.flags,
				reason,
			)
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return
}

func (vid *WrappedTx) WrappedInputs() (ret []WrappedOutput) {
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		ret = make([]WrappedOutput, v.Tx.NumInputs())
		v.ForEachInputDependency(func(i byte, inp *WrappedTx) bool {
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

func (vid *WrappedTx) SequencerPredecessor() (ret *WrappedTx) {
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		if seqData := v.Tx.SequencerTransactionData(); seqData != nil {
			ret = v.Inputs[seqData.SequencerOutputData.ChainConstraint.PredecessorInputIndex]
		}
	}})
	return
}

func (vid *WrappedTx) LinesTx(prefix ...string) *lines.Lines {
	ret := lines.New()
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret.Append(v.Tx.LinesShort(prefix...))
		},
		VirtualTx: func(v *VirtualTransaction) {
			ret.Add("a virtual tx %s", vid.IDShortString())
		},
		Deleted: func() {
			ret.Add("deleted tx %s", vid.IDShortString())
		},
	})
	return ret
}

func VerticesLines(vertices []*WrappedTx, prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for _, vid := range vertices {
		ret.Add(vid.String())
	}
	return ret
}

type _unwrapOptionsTraverse struct {
	UnwrapOptionsForTraverse
	visited set.Set[*WrappedTx]
}

// TraversePastConeDepthFirst performs depth-first traverse of the MemDAG. Visiting once each node
// and calling vertex-type specific function if provided on each.
// If function returns false, the traverse is cancelled globally.
// The traverse stops at terminal dag. The vertex is terminal if it either is not-full vertex
// i.e. (booked, orphaned, deleted) or it belongs to 'visited' set
// If 'visited' set is provided at call, it is mutable. In the end it contains all initial dag plus
// all dag visited during the traverse
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
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			v.ForEachInputDependency(func(i byte, inp *WrappedTx) bool {
				if inp == nil {
					return true
				}
				//util.Assertf(inp != nil, "_traversePastCone: input %d is nil (not solidified) in %s",
				//	i, func() any { return v.Tx.IDShortString() })
				ret = inp._traversePastCone(opt)
				return ret
			})
			if ret {
				v.ForEachEndorsement(func(i byte, inpEnd *WrappedTx) bool {
					if inpEnd == nil {
						return true
					}
					//util.Assertf(inpEnd != nil, "_traversePastCone: endorsement %d is nil (not solidified) in %s",
					//	i, func() any { return v.Tx.IDShortString() })
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
			if opt.Deleted != nil {
				ret = opt.Deleted(vid)
			}
		},
	})
	return ret
}

func (vid *WrappedTx) InflationAmountOfSequencerMilestone() (ret uint64) {
	util.Assertf(vid.IsSequencerMilestone(), "InflationAmountOfSequencerTx: not a sequencer milestone: %s", vid.IDShortString)
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.Tx.InflationAmount()
		},
		VirtualTx: func(v *VirtualTransaction) {
			seqOut, _ := v.SequencerOutputs()
			ret = seqOut.Inflation(vid.IsBranchTransaction())
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return
}

func (vid *WrappedTx) InflationConstraintOnSequencerOutput() (ret *ledger.InflationConstraint) {
	util.Assertf(vid.IsSequencerMilestone(), "InflationAmountOfSequencerOutput: not a sequencer milestone: %s", vid.IDShortString)

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret, _ = v.Tx.SequencerOutput().Output.InflationConstraint()
		},
		VirtualTx: func(v *VirtualTransaction) {
			seqOut, _ := v.SequencerOutputs()
			ret, _ = seqOut.InflationConstraint()
		},
		Deleted: vid.PanicAccessDeleted,
	})
	return
}

// UnwrapVirtualTx calls callback only if it is virtualTx
func (vid *WrappedTx) UnwrapVirtualTx(unwrapFun func(v *VirtualTransaction)) {
	vid.Unwrap(UnwrapOptions{
		VirtualTx: func(v *VirtualTransaction) {
			unwrapFun(v)
		},
	})
}

func (vid *WrappedTx) SetAttachmentDepthNoLock(depth int) {
	vid.attachmentDepth = depth
}

func (vid *WrappedTx) GetAttachmentDepthNoLock() int {
	return vid.attachmentDepth
}
