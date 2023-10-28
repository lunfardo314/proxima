package utangle

import (
	"fmt"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

func (v *Vertex) TimeSlot() core.TimeSlot {
	return v.Tx.ID().TimeSlot()
}

func (v *Vertex) getSequencerPredecessor() *WrappedTx {
	util.Assertf(v.Tx.IsSequencerMilestone(), "v.Tx.IsSequencerMilestone()")
	predIdx := v.Tx.SequencerTransactionData().SequencerOutputData.ChainConstraint.PredecessorInputIndex
	return v.Inputs[predIdx]
}

// getConsumedOutput return consumed output at index i or nil, nil if input is orphaned
func (v *Vertex) getConsumedOutput(i byte) (*core.Output, error) {
	if int(i) >= len(v.Inputs) {
		return nil, fmt.Errorf("wrong input index %d", i)
	}
	if v.Inputs[i] == nil {
		return nil, fmt.Errorf("input not solid at index %d", i)
	}
	return v.Inputs[i].OutputAt(v.Tx.MustOutputIndexOfTheInput(i))
}

func (v *Vertex) Validate() error {
	traceOption := transaction.TraceOptionFailedConstraints
	ctx, err := transaction.ContextFromTransaction(v.Tx, v.getConsumedOutput, traceOption)
	if err != nil {
		return err
	}
	return ctx.Validate()
}

func (v *Vertex) ValidateDebug() (string, error) {
	ctx, err := transaction.ContextFromTransaction(v.Tx, v.getConsumedOutput)
	if err != nil {
		return "", err
	}
	return ctx.String(), ctx.Validate()
}

// MissingInputTxIDSet return set of txids for the missing inputs
func (v *Vertex) MissingInputTxIDSet() set.Set[core.TransactionID] {
	ret := set.New[core.TransactionID]()
	for i, d := range v.Inputs {
		if d == nil {
			oid := v.Tx.MustInputAt(byte(i))
			ret.Insert(oid.TransactionID())
		}
	}
	for i, d := range v.Endorsements {
		if d == nil {
			ret.Insert(v.Tx.EndorsementAt(byte(i)))
		}
	}
	return ret
}

func (v *Vertex) MissingInputTxIDString() string {
	s := v.MissingInputTxIDSet()
	if len(s) == 0 {
		return "(none)"
	}
	ret := make([]string, 0)
	for txid := range s {
		ret = append(ret, txid.Short())
	}
	return strings.Join(ret, ", ")
}

func (v *Vertex) IsSolid() bool {
	return v.isSolid
}

func (v *Vertex) _isSolid() bool {
	for _, d := range v.Inputs {
		if d == nil {
			return false
		}
	}
	for _, d := range v.Endorsements {
		if d == nil {
			return false
		}
	}
	return true
}

func (v *Vertex) MustProducedOutput(idx byte) (*core.Output, bool) {
	odata, ok := v.producedOutputData(idx)
	if !ok {
		return nil, false
	}
	o, err := core.OutputFromBytesReadOnly(odata)
	util.AssertNoError(err)
	return o, true
}

func (v *Vertex) producedOutputData(idx byte) ([]byte, bool) {
	if int(idx) >= v.Tx.NumProducedOutputs() {
		return nil, false
	}
	return v.Tx.MustOutputDataAt(idx), true
}

func (v *Vertex) StemOutput() *core.OutputWithID {
	util.Assertf(v.Tx.IsSequencerMilestone(), "v.Tx.SequencerFlagON()")
	seqMeta := v.Tx.SequencerTransactionData()
	o, ok := v.MustProducedOutput(seqMeta.StemOutputIndex)
	util.Assertf(ok, "can't get stem output")
	return &core.OutputWithID{
		ID:     v.Tx.OutputID(seqMeta.StemOutputIndex),
		Output: o,
	}
}

func (v *Vertex) SequencerID() core.ChainID {
	util.Assertf(v.Tx.IsSequencerMilestone(), "v.Tx.SequencerFlagON()")
	seqMeta := v.Tx.SequencerTransactionData()
	return seqMeta.SequencerID
}

// SequencerMilestonePredecessorOutputID returns with .Vertex == nil if predecessor is finalized
func (v *Vertex) SequencerMilestonePredecessorOutputID() core.OutputID {
	util.Assertf(v.Tx.IsSequencerMilestone(), "v.Tx.SequencerFlagON()")
	predOutIdx := v.Tx.SequencerTransactionData().SequencerOutputData.ChainConstraint.PredecessorInputIndex
	return v.Tx.MustInputAt(predOutIdx)
}

func (v *Vertex) forEachInputDependency(fun func(i byte, vidInput *WrappedTx) bool) {
	for i, inp := range v.Inputs {
		if !fun(byte(i), inp) {
			return
		}
	}
}

func (v *Vertex) forEachEndorsement(fun func(i byte, vidEndorsed *WrappedTx) bool) {
	for i, vEnd := range v.Endorsements {
		if !fun(byte(i), vEnd) {
			return
		}
	}
}

func (v *Vertex) String() string {
	return v.Lines().String()
}

func (v *Vertex) Lines(prefix ...string) *lines.Lines {
	return v.Tx.Lines(func(i byte) (*core.Output, error) {
		if v.Inputs[i] == nil {
			return nil, fmt.Errorf("input #%d not solid", i)
		}
		inpOid, err := v.Tx.InputAt(i)
		if err != nil {
			return nil, fmt.Errorf("input #%d: %v", i, err)
		}
		return v.Inputs[i].OutputAt(inpOid.Index())
	}, prefix...)
}

func (v *Vertex) ConsumedInputsToString() string {
	return v.ConsumedInputsToLines().String()
}

func (v *Vertex) ConsumedInputsToLines() *lines.Lines {
	ret := lines.New()
	ret.Add("Consumed outputs (%d) of vertex %s", v.Tx.NumInputs(), v.Tx.IDShort())
	for i, dep := range v.Inputs {
		id, err := v.Tx.InputAt(byte(i))
		util.AssertNoError(err)
		if dep == nil {
			ret.Add("   %d %s : not solid", i, id.Short())
		} else {
			o, err := dep.OutputAt(byte(i))
			if err == nil {
				if o != nil {
					ret.Add("   %d %s : \n%s", i, id.Short(), o.ToString("     "))
				} else {
					ret.Add("   %d %s : (not available)", i, id.Short())
				}
			} else {
				ret.Add("   %d %s : %v", i, id.Short(), err)
			}
		}
	}
	return ret
}

func (v *Vertex) Wrap() *WrappedTx {
	return _newVID(_vertex{
		Vertex:      v,
		whenWrapped: time.Now(),
	})
}

func (v *Vertex) convertToVirtualTx() *VirtualTransaction {
	ret := &VirtualTransaction{
		txid:    *v.Tx.ID(),
		outputs: make(map[byte]*core.Output, v.Tx.NumProducedOutputs()),
	}
	if v.Tx.IsSequencerMilestone() {
		seqIdx, stemIdx := v.Tx.SequencerAndStemOutputIndices()
		ret.sequencerOutputs = &[2]byte{seqIdx, stemIdx}
	}

	v.Tx.ForEachProducedOutput(func(idx byte, o *core.Output, _ *core.OutputID) bool {
		ret.outputs[idx] = o
		return true
	})
	return ret
}

func (v *Vertex) PendingDependenciesLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)

	ret.Add("not solid inputs:")
	v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
		if inp == nil {
			oid := v.Tx.MustInputAt(i)
			ret.Add("   %d : %s", i, oid.Short())
		}
		return true
	})
	ret.Add("not solid endorsements:")
	v.forEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
		if vEnd == nil {
			txid := v.Tx.EndorsementAt(i)
			ret.Add("   %d : %s", i, txid.Short())
		}
		return true
	})
	return ret
}

func (v *Vertex) addFork(f Fork) bool {
	if v.pastTrack.forks == nil {
		v.pastTrack.forks = make(ForkSet)
	}
	return v.pastTrack.forks.Insert(f)
}

func (v *Vertex) reMergeParentPastTracks() (conflict *WrappedOutput) {
	v.forEachInputDependency(func(i byte, vidInput *WrappedTx) bool {
		util.Assertf(vidInput != nil, "vidInput != nil")
		conflict = v.pastTrack.AbsorbPastTrack(vidInput)
		return conflict == nil
	})

	if conflict != nil {
		return
	}
	v.forEachEndorsement(func(_ byte, vidEndorsed *WrappedTx) bool {
		util.Assertf(vidEndorsed != nil, "vidEndorsed != nil")
		conflict = v.pastTrack.AbsorbPastTrack(vidEndorsed)
		return conflict == nil
	})
	return
}

func (p *PastTrack) clone() PastTrack {
	return PastTrack{
		forks:          p.forks.Clone(),
		baselineBranch: p.baselineBranch,
	}
}

func (p *PastTrack) absorb(p1 *PastTrack) *WrappedOutput {
	util.Assertf(p != nil, "p!=nil")

	if p.forks == nil {
		p.forks = make(ForkSet)
	}

	var success bool
	if p.baselineBranch, success = mergeBranches(p.baselineBranch, p1.baselineBranch); !success {
		return &WrappedOutput{}
	}

	if conflict := p.forks.Absorb(p1.forks); conflict.VID != nil {
		return &conflict
	}
	return nil
}

func (p *PastTrack) AbsorbPastTrack(vid *WrappedTx) (conflict *WrappedOutput) {
	return p._absorbPastTrack(vid, false)
}

func (p *PastTrack) AbsorbPastTrackSafe(vid *WrappedTx) (conflict *WrappedOutput) {
	return p._absorbPastTrack(vid, true)
}

func (p *PastTrack) _absorbPastTrack(vid *WrappedTx, safe bool) (conflict *WrappedOutput) {
	var success bool
	var baselineBranch *WrappedTx
	var wrappedConflict WrappedOutput

	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		if vid.IsBranchTransaction() {
			baselineBranch, success = mergeBranches(p.baselineBranch, vid)
		} else {
			baselineBranch, success = mergeBranches(p.baselineBranch, vid.BaselineBranch())
		}
		if !success {
			conflict = &WrappedOutput{}
			return
		}
		if p.forks == nil {
			p.forks = make(ForkSet)
		}
		if safe {
			wrappedConflict = p.forks.AbsorbSafe(v.pastTrack.forks)
		} else {
			wrappedConflict = p.forks.Absorb(v.pastTrack.forks)
		}
		if wrappedConflict.VID != nil {
			conflict = &wrappedConflict
			return
		}
		p.baselineBranch = baselineBranch
	}})
	return
}

func (p *PastTrack) BaselineBranch() *WrappedTx {
	return p.baselineBranch
}

func (p *PastTrack) MustGetBaselineState(ut *UTXOTangle) general.IndexedStateReader {
	return ut.MustGetBaselineState(p.BaselineBranch())
}

func (p *PastTrack) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	if p == nil {
		ret.Add("<nil>")
	} else {
		if p.baselineBranch == nil {
			ret.Add("----- baseline branch: <nil>")
		} else {
			ret.Add("----- baseline branch: %s", p.baselineBranch.IDShort())
		}
		ret.Add("---- forks")
		ret.Append(p.forks.Lines())
	}
	return ret
}

// BaselineBranch is the latest branch vertex the current vertex is descendent of.
// Vertex is not necessarily solid. For pending inputs and endorsements are considered nil branch tx.
// If v is not a sequencer milestone, or it is a virtual transaction, BaselineBranch == nil
// If v is a branch itself, the BaselineBranch is the predecessor branch
func (v *Vertex) BaselineBranch() *WrappedTx {
	return v.pastTrack.BaselineBranch()
}
