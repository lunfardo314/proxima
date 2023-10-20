package utangle

import (
	"fmt"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/core"
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

func (v *Vertex) mergeInputDeltas(ut *UTXOTangle) error {
	util.Assertf(v.IsSolid(), "some inputs are not solid")
	deltas := make([]*UTXOStateDelta, 0, v.Tx.NumInputs()+v.Tx.NumEndorsements())

	// traverse tx inputs AND endorsements
	v.forEachDependency(func(inp *WrappedTx) bool {
		deltas = append(deltas, inp.GetUTXOStateDelta())
		return true
	})

	delta, conflict := ut.MergeDeltas(deltas...)
	if conflict != nil {
		return fmt.Errorf("conflict in the past cone at %s", conflict.IDShort())
	}
	util.Assertf(delta.utxoStateDelta != nil, "delta.utxoStateDelta != nil")
	v.StateDelta = *delta
	return nil
}

func (v *Vertex) CalcDeltaAndWrap(ut *UTXOTangle) (*WrappedTx, error) {
	if err := v.mergeInputDeltas(ut); err != nil {
		return nil, err
	}

	vid := v.Wrap()
	vid.MustConsistentDelta(ut)

	cloneTmp := v.StateDelta.Clone()

	const printBeforeAfter = false

	if conflict := v.StateDelta.Include(vid, ut.MustGetStateReader); conflict.VID != nil {
		if printBeforeAfter {
			fmt.Printf("before ==\n%s\n== after\n%s\n", cloneTmp.Lines().String(), v.StateDelta.Lines().String())
		}
		return vid, fmt.Errorf("conflict %s while including %s into delta", conflict.IDShort(), vid.IDShort())
	}
	vid.MustConsistentDelta(ut)

	return vid, nil
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

func (v *Vertex) Validate(bypassConstraintValidation ...bool) error {
	traceOption := transaction.TraceOptionFailedConstraints
	bypass := len(bypassConstraintValidation) > 0 && !bypassConstraintValidation[0]
	if bypass {
		traceOption = transaction.TraceOptionNone
	}
	ctx, err := transaction.ContextFromTransaction(v.Tx, v.getConsumedOutput, traceOption)
	if err != nil {
		return err
	}
	if bypass {
		return nil
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
	util.Assertf(!v.Tx.IsSequencerMilestone() || v.StateDelta.branchTxID != nil, "inconsistency: unknown baseline branch in the solid sequencer transaction")
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

func (v *Vertex) forEachInputDependency(fun func(i byte, inp *WrappedTx) bool) {
	for i, inp := range v.Inputs {
		if !fun(byte(i), inp) {
			return
		}
	}
}

func (v *Vertex) forEachEndorsement(fun func(i byte, vEnd *WrappedTx) bool) {
	for i, vEnd := range v.Endorsements {
		if !fun(byte(i), vEnd) {
			return
		}
	}
}

// forEachDependency traverses first all input links, then all endorsements
func (v *Vertex) forEachDependency(fun func(inp *WrappedTx) bool) {
	v.forEachInputDependency(func(_ byte, inp *WrappedTx) bool {
		return fun(inp)
	})
	v.forEachEndorsement(func(_ byte, inp *WrappedTx) bool {
		return fun(inp)
	})
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
