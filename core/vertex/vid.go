package vertex

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

func (v _vertex) _outputAt(idx byte) (*ledger.Output, error) {
	return v.ProducedOutputAt(idx)
}

func (v _detachedVertex) _outputAt(idx byte) (*ledger.Output, error) {
	return v.ProducedOutputAt(idx)
}

func (v _virtualTx) _outputAt(idx byte) (*ledger.Output, error) {
	if o, available := v.OutputAt(idx); available {
		return o, nil
	}
	return nil, nil
}

func _newVID(g _genericVertex, txid base.TransactionID, seqID *base.ChainID) *WrappedTx {
	ret := &WrappedTx{
		id:             txid,
		_genericVertex: g,
	}
	ret.SequencerID.Store(seqID)
	ret.OnPokeNop()
	return ret
}

func (vid *WrappedTx) _put(g _genericVertex) {
	vid._genericVertex = g
}

func (vid *WrappedTx) ID() base.TransactionID {
	return vid.id
}

func (vid *WrappedTx) FlagsNoLock() Flags {
	return vid.flags
}

func (vid *WrappedTx) SetFlagsUpNoLock(f Flags) {
	vid.flags = vid.flags | f
}

func (vid *WrappedTx) SetFlagsDownNoLock(f Flags) {
	vid.flags = vid.flags & ^f
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
	util.Assertf(vid.id == v.ID(), "ConvertVirtualTxToVertexNoLock: txid-s do not match in: %s", vid.id.StringShort)
	_, isVirtualTx := vid._genericVertex.(_virtualTx)
	util.Assertf(isVirtualTx, "ConvertVirtualTxToVertexNoLock: virtual tx target expected %s", vid.id.StringShort)
	vid._put(_vertex{Vertex: v})
	if v.IsSequencerTransaction() {
		vid.SequencerID.Store(util.Ref(v.SequencerTransactionData().SequencerID))
	}
}

// ReattachVertexNoLock converts a DetachedVertex back to a fresh Vertex for re-solidification.
// Resets all mutable flags and status — the vertex becomes Undefined with clean state.
// Re-parses the transaction from raw bytes to get a fresh partial-context copy,
// because the original *transaction.Transaction may already have full context set
// (SetFullContext was called during the first attachment), and SetFullContext asserts
// it can only be called once.
// Must be called under write lock (inside Unwrap).
// The consumed map is preserved — consumer tracking from before detachment remains valid.
func (vid *WrappedTx) ReattachVertexNoLock(tx *transaction.Transaction) {
	freshTx, err := transaction.ParseWithPartialValidation(tx.Bytes())
	util.AssertNoError(err)
	vid._put(_vertex{NewVertex(freshTx)})
	vid.flags = FlagVertexTxAttachmentStarted
	vid.err = nil
	vid.pastCone = nil // already nil after detachment, explicit for clarity
	vid.coverage.Store(nil)
}

// ConvertToDetached detaches past cone and leaves only a collection of produced outputs
// Detaches input dependencies and converts to the DetachedVertex
// Note, however, that for branches, WrappedTx with DetachedVertex can later contain reference to the pastCone structure.
// Upon repeated calls to ConvertToDetached we set pastCone to nil. If not this, DAG remains always connected and
// old vertices are not garbage collected -> memory leak!
func (vid *WrappedTx) ConvertToDetached() {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if vid.FlagsUpNoLock(FlagVertexTxAttachmentStarted) && !vid.FlagsUpNoLock(FlagVertexTxAttachmentFinished) {
				// Attacher is actively processing this vertex — do not detach.
				// Without this guard, GC can oscillate the vertex between Vertex and
				// DetachedVertex, causing duplicate reattachments that panic on
				// MarkWorkProcessStarted.
				return
			}
			vid.convertToDetachedTxUnlocked(v)
			vid.pastCone = nil
		},
		DetachedVertex: func(v *DetachedVertex) {
			util.Assertf(vid.pastCone == nil || vid.IsBranchTransaction(), "vid.pastCone == nil ||vid.IsBranchTransaction()")
			vid.pastCone = nil // Important: if not this, memdag leaks memory. Later: why exactly?
		},
		VirtualTx: func(v *VirtualTransaction) {
			util.Assertf(vid.pastCone == nil, "vid.pastCone == nil")
		},
	})
}

func (vid *WrappedTx) convertToDetachedTxUnlocked(v *Vertex) {
	vid._put(_detachedVertex{v.toDetachedVertex()})
	v.UnReferenceDependencies()
	vid.OnPokeNop()
	vid.SetFlagsUpNoLock(FlagVertexIgnoreAbsenceOfPastCone)
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

func (vid *WrappedTx) GetPastConeNoLock() *PastConeBase {
	return vid.pastCone
}

// PastConeSize returns the cached past cone's vertex count without taking the mutex,
// or 0 if the past cone is not attached. Used for diagnostic purposes only; a concurrent
// SetTxStatusGood may race but the diagnostic tolerates a stale read.
func (vid *WrappedTx) PastConeSize() int {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()
	if vid.pastCone == nil {
		return 0
	}
	return vid.pastCone.Len()
}

// SetTxStatusGood sets 'good' status and past cone
func (vid *WrappedTx) SetTxStatusGood(pastCone *PastConeBase, coverage uint64) {
	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	vid.SetTxStatusGoodNoLock(pastCone, coverage)
}

func (vid *WrappedTx) SetTxStatusGoodNoLock(pastCone *PastConeBase, coverage uint64) {
	util.Assertf(vid.GetTxStatusNoLock() != Bad, "vid.GetTxStatusNoLock() != Bad (%s)", vid.StringNoLock)

	vid.flags.SetFlagsUp(FlagVertexDefined)
	if pastCone == nil {
		vid.flags.SetFlagsUp(FlagVertexIgnoreAbsenceOfPastCone)
	} else {
		vid.pastCone = pastCone
		if coverage > 0 {
			vid.coverage.Store(util.Ref(coverage))
		}
	}
}

func (vid *WrappedTx) SetSequencerAttachmentFinished() {
	util.Assertf(vid.IsSequencerTransaction(), "vid.IsSequencerTransaction()")

	vid.mutex.Lock()
	defer vid.mutex.Unlock()

	util.Assertf(vid.flags.FlagsUp(FlagVertexTxAttachmentStarted), "vid.flags.FlagsUp(FlagVertexTxAttachmentStarted)")
	vid.flags.SetFlagsUp(FlagVertexTxAttachmentFinished)
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

// IsBad non-deterministic
func (vid *WrappedTx) IsBad() bool {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.GetTxStatusNoLock() == Bad
}

func (vid *WrappedTx) OnPoke(fun func()) {
	vid.onPoke.Store(fun)
	//if fun == nil {
	//	vid.onPoke.Store(func() {})
	//} else {
	//	vid.onPoke.Store(fun)
	//}
}

var _nopFun = func() {}

func (vid *WrappedTx) OnPokeNop() {
	vid.onPoke.Store(_nopFun)
}

func (vid *WrappedTx) Poke() {
	vid.onPoke.Load().(func())()
}

// WrapTxID creates VID with a virtualTx which only contains txid.
// Also sets the solidification deadline, after which IsPullDeadlineDue will start returning true
// The pull deadline will be dropped after transaction becomes available and virtualTx will be converted
// to full vertex
func WrapTxID(txid base.TransactionID) *WrappedTx {
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
		DetachedVertex: func(v *DetachedVertex) {
			mode = "detachedTx"
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
	})
	return fmt.Sprintf("%22s %10s (%s%s) %s, onPoke = %p, added %d slots back",
		vid.IDShortString(), mode, status, flagsStr, reason,
		vid.onPoke.Load(), ledger.TimeNow().Slot-vid.SlotWhenAdded)
}

func (vid *WrappedTx) IDShortString() string {
	return vid.id.StringShort()
}

func (vid *WrappedTx) IDVeryShort() string {
	return vid.id.StringVeryShort()
}

func (vid *WrappedTx) IsBranchTransaction() bool {
	return vid.id.IsBranchTransaction()
}

func (vid *WrappedTx) IsSequencerTransaction() bool {
	return vid.id.IsSequencerTransaction()
}

func (vid *WrappedTx) Timestamp() base.LedgerTime {
	return vid.id.Timestamp()
}

func (vid *WrappedTx) Before(vid1 *WrappedTx) bool {
	return vid.Timestamp().Before(vid1.Timestamp())
}

func (vid *WrappedTx) Slot() uint32 {
	return vid.id.Slot()
}

func (vid *WrappedTx) OutputWithIDAt(idx byte) (ledger.OutputWithID, error) {
	ret, err := vid.OutputAt(idx)
	if err != nil || ret == nil {
		return ledger.OutputWithID{}, err
	}
	return ledger.OutputWithID{
		ID:     base.MustNewOutputID(vid.id, idx),
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

func (vid *WrappedTx) MustSequencerIDAndStemID() (seqID base.ChainID, stemID base.OutputID) {
	util.Assertf(vid.IsBranchTransaction(), "vid.IsBranchTransaction()")
	p := vid.SequencerID.Load()
	util.Assertf(p != nil, "sequencerID is must be not nil")
	seqID = *p
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			stemID = vid.OutputID(v.SequencerTransactionData().StemOutputIndex)
		},
		DetachedVertex: func(v *DetachedVertex) {
			stemID = vid.OutputID(v.SequencerTransactionData().StemOutputIndex)
		},
		VirtualTx: func(v *VirtualTransaction) {
			util.Assertf(v.sequencerOutputIndices != nil, "v.sequencerOutputs != nil")
			stemID = vid.OutputID(v.sequencerOutputIndices[1])
		},
	})
	return
}

func (vid *WrappedTx) SequencerWrappedOutput() (ret WrappedOutput) {
	util.Assertf(vid.IsSequencerTransaction(), "vid.IsSequencerTransaction()")

	var seqData *ledger.SequencerTransactionData
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			seqData = v.SequencerTransactionData()
			util.Assertf(seqData != nil, "seqData is nil")
			ret = WrappedOutput{
				VID:   vid,
				Index: seqData.SequencerOutputIndex,
			}
		},
		DetachedVertex: func(v *DetachedVertex) {
			seqData = v.SequencerTransactionData()
			util.Assertf(seqData != nil, "seqData is nil")
			ret = WrappedOutput{
				VID:   vid,
				Index: seqData.SequencerOutputIndex,
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

func (vid *WrappedTx) FindChainOutput(chainID *base.ChainID) (ret *ledger.OutputWithID) {
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.FindChainOutput(*chainID)
		},
		DetachedVertex: func(v *DetachedVertex) {
			ret = v.FindChainOutput(*chainID)
		},
		VirtualTx: func(v *VirtualTransaction) {
			ret = v.findChainOutput(vid.id, chainID)
		},
	})
	return
}

func (vid *WrappedTx) StemWrappedOutput() (ret WrappedOutput) {
	util.Assertf(vid.IsBranchTransaction(), "vid.IsBranchTransaction()")

	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if seqData := v.SequencerTransactionData(); seqData != nil {
				ret = WrappedOutput{
					VID:   vid,
					Index: v.SequencerTransactionData().StemOutputIndex,
				}
			}
		},
		DetachedVertex: func(v *DetachedVertex) {
			if seqData := v.SequencerTransactionData(); seqData != nil {
				ret = WrappedOutput{
					VID:   vid,
					Index: v.SequencerTransactionData().StemOutputIndex,
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
	})
	return
}

func (vid *WrappedTx) OutputID(idx byte) (ret base.OutputID) {
	ret = base.MustNewOutputID(vid.id, idx)
	return
}

// GetVertex returns the Vertex pointer under a brief read lock.
// Returns nil if the underlying type is not _vertex (DetachedVertex or VirtualTx).
// The returned pointer is safe to use after the lock is released when the caller
// guarantees no concurrent type change (FlagVertexTxAttachmentStarted is set).
func (vid *WrappedTx) GetVertex() *Vertex {
	vid.mutex.RLock()
	defer vid.mutex.RUnlock()

	return vid.GetVertexNoLock()
}

// GetVertexNoLock returns the Vertex pointer without locking.
// For use inside existing Unwrap/RUnwrap callbacks where the lock is already held.
func (vid *WrappedTx) GetVertexNoLock() *Vertex {
	if v, ok := vid._genericVertex.(_vertex); ok {
		return v.Vertex
	}
	return nil
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
	case _detachedVertex:
		if opt.DetachedVertex != nil {
			opt.DetachedVertex(v.DetachedVertex)
		}
	case _virtualTx:
		if opt.VirtualTx != nil {
			opt.VirtualTx(v.VirtualTransaction)
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
		DetachedVertex: func(v *DetachedVertex) {
			ret.Add("== detached vertex %s", vid.IDShortString())
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
	})
	return ret
}

func (vid *WrappedTx) LinesNoLock(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("id: %s", vid.id.StringShort()).
		Add("Kind: %s", vid._ofKindString()).
		Add("Status: %s", vid.GetTxStatusNoLock().String()).
		Add("Flags: %s", vid.flags.String()).
		Add("Err: %v", vid.err)
	if seqID := vid.SequencerID.Load(); seqID == nil {
		ret.Add("Seq id: <nil>")
	} else {
		ret.Add("Seq id: %s", seqID.StringShort())
	}
	switch v := vid._genericVertex.(type) {
	case _vertex:
		ret.Add("---- transaction ----\n" + v.LinesShort(prefix...).String())
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
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.NumInputs()
		},
		DetachedVertex: func(v *DetachedVertex) {
			ret = v.NumInputs()
		},
	})
	return ret
}

func (vid *WrappedTx) NumProducedOutputs() int {
	return vid.id.NumProducedOutputs()
}

// BaselineBranch baseline branch of the vertex
func (vid *WrappedTx) BaselineBranch() (baselineBranchID base.TransactionID, ok bool) {
	if vid.id.IsBranchTransaction() {
		return vid.id, true
	}
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if v.BaselineBranchID != nil {
				baselineBranchID = *v.BaselineBranchID
				ok = true
			}
		},
		DetachedVertex: func(v *DetachedVertex) {
			// it means tx was already attached and vertex does not contain reference to the baseline reference.
			util.Assertf(v.BranchID.IsBranchTransaction(), "v.BranchID.IsBranchTransaction()")
			if v.BranchID != nil {
				baselineBranchID = *v.BranchID
				ok = true
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			util.Panicf("BaselineBranchID(%s): can't access baseline branch in virtual tx", vid.IDShortString())
		},
	})
	return
}

func (vid *WrappedTx) MustEnsureOutput(o *ledger.Output, idx byte) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			util.Assertf(bytes.Equal(o.Bytes(), v.MustProducedOutputAt(idx).Bytes()),
				"MustEnsureOutput: inconsistent output data in %s",
				func() string { return util.Ref(v.OutputID(idx)).StringShort() })
		},
		DetachedVertex: func(v *DetachedVertex) {
			util.Assertf(bytes.Equal(o.Bytes(), v.MustProducedOutputAt(idx).Bytes()),
				"MustEnsureOutput: inconsistent output data in %s",
				func() string { return util.Ref(v.OutputID(idx)).StringShort() })
		},
		VirtualTx: func(v *VirtualTransaction) {
			v.mustAddOutput(idx, o)
		},
	})
}

// AddConsumer stores consumer of the vid[outputIndex] consumed output.
// Function checkConflicts checks if the new consumer conflicts with already existing ones
func (vid *WrappedTx) AddConsumer(outputIndex byte, consumer *WrappedTx) {
	util.Assertf(int(outputIndex) < vid.NumProducedOutputs(), "wrong output index")

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
}

func (vid *WrappedTx) WithConsumersRLock(fun func()) {
	vid.mutexDescendants.RLock()
	fun()
	vid.mutexDescendants.RUnlock()
}

func (vid *WrappedTx) NotConsumedOutputIndices(allConsumers set.Set[*WrappedTx]) []byte {
	vid.mutexDescendants.Lock()
	defer vid.mutexDescendants.Unlock()

	nOutputs := 0
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			nOutputs = v.NumProducedOutputs()
		},
		DetachedVertex: func(v *DetachedVertex) {
			nOutputs = v.NumProducedOutputs()
		},
	})

	ret := make([]byte, 0, nOutputs)

	for i := 0; i < nOutputs; i++ {
		if set.DoNotIntersect(vid.consumed[byte(i)], allConsumers) {
			ret = append(ret, byte(i))
		}
	}
	return ret
}

// GetLedgerCoverageP returns pointer to coverage value, nil if not set.
// Coverage is stored as atomic pointer — lock-free, safe to call concurrently
// with ConvertToDetached and other vertex mutations. This eliminates the
// RLock contention between tippool reads and memDAG pruning writes.
func (vid *WrappedTx) GetLedgerCoverageP() *uint64 {
	return vid.coverage.Load()
}

// GetLedgerCoverage returns coverage value, 0 if not set. Lock-free (atomic).
func (vid *WrappedTx) GetLedgerCoverage() uint64 {
	ret := vid.coverage.Load()
	if ret == nil {
		return 0
	}
	return *ret
}

func (vid *WrappedTx) GetLedgerCoverageString() string {
	if vid == nil {
		return "n/a"
	}
	return util.Th(vid.GetLedgerCoverage())
}

// NumConsumers returns:
// - number of consumed outputs
// - number of conflict sets
func (vid *WrappedTx) NumConsumers() (numConsumedOutputs, numConflictSets int) {
	vid.WithConsumersRLock(func() {
		numConsumedOutputs = len(vid.consumed)
		for _, ds := range vid.consumed {
			if len(ds) > 1 {
				numConflictSets++
			}
		}
	})
	return
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
			if p := vid.coverage.Load(); p != nil {
				cov = *p
			}
			t := "vertex (" + vid.GetTxStatusNoLock().String() + ")"
			ret = fmt.Sprintf("%20s %s :: in: %d, out: %d, consumed: %d, conflicts: %d, Flags: %08b, err: '%v', cov: %s",
				t,
				vid.id.StringShort(),
				v.NumInputs(),
				v.NumProducedOutputs(),
				consumed,
				doubleSpent,
				vid.flags,
				reason,
				util.Th(cov),
			)
		},
		DetachedVertex: func(v *DetachedVertex) {
			cov := uint64(0)
			if p := vid.coverage.Load(); p != nil {
				cov = *p
			}
			t := "vertex (" + vid.GetTxStatusNoLock().String() + ")"
			ret = fmt.Sprintf("%20s %s :: in: %d, out: %d, consumed: %d, conflicts: %d, Flags: %08b, err: '%v', cov: %s",
				t,
				vid.id.StringShort(),
				v.NumInputs(),
				v.NumProducedOutputs(),
				consumed,
				doubleSpent,
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
				vid.id.StringShort(),
				len(v.outputs),
				consumed,
				doubleSpent,
				vid.flags,
				reason,
			)
		},
	})
	return
}

// SequencerPredecessor returns the predecessor vertex of a sequencer transaction.
// For a DetachedVertex, reattachBranch is called with the branch ID (not vid's own ID),
// so there is no self-deadlock risk — AttachTxID locks a different vertex.
// For non-branch detached vertices, BranchID is nil and ret stays nil (caller handles this).
func (vid *WrappedTx) SequencerPredecessor(reattachBranch func(txid base.TransactionID) *WrappedTx) (ret *WrappedTx) {
	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			if seqData := v.SequencerTransactionData(); seqData != nil {
				ret = v.Inputs[seqData.SequencerOutputData.ChainConstraint.PredecessorInputIndex]
			}
		},
		DetachedVertex: func(v *DetachedVertex) {
			if v.BranchID != nil {
				ret = reattachBranch(*v.BranchID)
			}
		},
	})
	return
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
		DetachedVertex: func(v *DetachedVertex) {
			if ret && opt.DetachedVertex != nil {
				ret = opt.DetachedVertex(vid, v)
			}
		},
		VirtualTx: func(v *VirtualTransaction) {
			if opt.VirtualTx != nil {
				ret = opt.VirtualTx(vid, v)
			}
		},
	})
	return ret
}

// InflationAmount is sum of inflation values on the produced outputs
func (vid *WrappedTx) InflationAmount() (ret uint64) {
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.InflationAmount()
		},
		DetachedVertex: func(v *DetachedVertex) {
			ret = v.InflationAmount()
		},
		VirtualTx: func(v *VirtualTransaction) {
			ret = v.inflation
		},
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

func (vid *WrappedTx) IDHasFragment(frag ...string) bool {
	for _, fr := range frag {
		if strings.Contains(vid.id.String(), fr) {
			return true
		}
	}
	return false
}

func (vid *WrappedTx) forEachConsumerNoLock(fun func(consumer *WrappedTx, outputIndex byte) bool) {
	for idx, consumers := range vid.consumed {
		for consumer := range consumers {
			if !fun(consumer, idx) {
				return
			}
		}
	}
}

func (vid *WrappedTx) SequencerName() (ret string) {
	ret = "(N/A)"
	if tx := vid.GetTransaction(); tx != nil {
		if seqData := tx.SequencerTransactionData(); seqData != nil {
			if outData := seqData.SequencerOutputData; outData != nil {
				if outData.SequencerData != nil {
					ret = outData.SequencerData.Name()
				}
			}
		}
	}
	return
}

func (vid *WrappedTx) SequencerTransactionData() (ret *ledger.SequencerTransactionData) {
	if tx := vid.GetTransaction(); tx != nil {
		ret = tx.SequencerTransactionData()
	}
	return
}

func (vid *WrappedTx) GetTransaction() (tx *transaction.Transaction) {
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			tx = v.Transaction
		},
		DetachedVertex: func(v *DetachedVertex) {
			tx = v.Transaction
		},
	})
	return
}

func (vid *WrappedTx) ValidSequencerPace(targetTs base.LedgerTime) bool {
	return ledger.ValidSequencerPace(vid.Timestamp(), targetTs)
}

func (vid *WrappedTx) AttachmentCost() (ret int) {
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			ret = v.AttachmentCost()
			return
		},
		DetachedVertex: func(v *DetachedVertex) {
			ret = v.AttachmentCost()
			return
		},
	})
	return
}
