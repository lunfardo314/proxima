package utangle

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/core"
	state "github.com/lunfardo314/proxima/multistate"
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

func (v *Vertex) mergeInputDeltas() error {
	var err error
	v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
		inp.Unwrap(UnwrapOptions{
			Vertex: func(vInp *Vertex) {
				conflict, consumer := vInp.StateDelta.MergeInto(&v.StateDelta)
				if conflict != nil {
					err = fmt.Errorf("conflict %s while including tx %s into delta:\n%s", conflict.IDShort(), consumer.IDShort(), v.StateDelta.LinesRecursive().String())
				}
			},
			VirtualTx: func(_ *VirtualTransaction) {
				v.StateDelta.include(inp)
			},
		})
		return err == nil
	})
	if err != nil {
		return err
	}

	v.forEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
		vEnd.Unwrap(UnwrapOptions{
			Vertex: func(vInp *Vertex) {
				conflict, consumer := vInp.StateDelta.MergeInto(&v.StateDelta)
				if conflict != nil {
					err = fmt.Errorf("conflict %s while including tx %s into delta:\n%s",
						conflict.IDShort(), consumer.IDShort(), v.StateDelta.LinesRecursive().String())
				}
			}, VirtualTx: func(_ *VirtualTransaction) {
				v.StateDelta.include(vEnd)
			}})
		return err == nil
	})
	return err
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
	if v.Tx.IsSequencerMilestone() && v.StateDelta.baselineBranch == nil {
		return false
	}
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

func (v *Vertex) Logf(format string, args ...any) {
	v.txLog.Logf(format, args...)
}

func (v *Vertex) WriteLog(w io.Writer) {
	v.txLog.WriteLog(w)
}

func (v *Vertex) DumpLog() string {
	var buf strings.Builder
	v.WriteLog(&buf)
	return buf.String()
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

func (v *Vertex) String() string {
	return v.Lines().String()
}

func (v *Vertex) Lines(prefix ...string) *lines.Lines {
	ctx, err := transaction.ContextFromTransaction(v.Tx, func(i byte) (*core.Output, error) {
		if v.Inputs[i] == nil {
			return nil, fmt.Errorf("input #%d not solid", i)
		}
		inpOid, err := v.Tx.InputAt(i)
		if err != nil {
			return nil, fmt.Errorf("input #%d: %v", i, err)
		}
		return v.Inputs[i].OutputAt(inpOid.Index())
	})
	if err != nil {
		return lines.New(prefix...).Add("failed to create context of %s : %v", v.Tx.IDShort(), err)
	}
	return ctx.Lines(prefix...)
}

func (v *Vertex) ConsumedInputsToString() string {
	return v.ConsumedInputsToLines().String()
}

func (v *Vertex) ConsumedInputsToLines() *lines.Lines {
	ret := lines.New()
	ret.Add("Consumed outputs (%d) of vertex %s", v.Tx.NumInputs(), v.Tx.IDShort())
	if v.Tx.IsSequencerMilestone() {
		ret.Add("    branch: %s", v.StateDelta.baselineBranch.IDShort())
	}
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

func (v *Vertex) fetchBranchDependency(ut *UTXOTangle) error {
	// find a vertex which to follow towards branch transaction
	// If tx itself is a branch tx, it will point towards previous transaction in the sequencer chain
	// Each sequencer transaction belongs to a branch
	branchConeTipVertex, err := ut.getBranchConeTipVertex(v)
	if err != nil {
		// something wrong with the transaction
		return err
	}
	if branchConeTipVertex == nil {
		// the vertex has no solid root, cannot be solidified (yet or never)
		return nil
	}
	// vertex has solid branch
	util.Assertf(branchConeTipVertex.IsSequencerMilestone(), "branchConeTipVertex.Tx.IsSequencerMilestone()")

	if branchConeTipVertex.IsBranchTransaction() {
		util.Assertf(ut.isValidBranch(branchConeTipVertex), "ut.isValidBranch(branchConeTipVertex)")
		v.StateDelta.baselineBranch = branchConeTipVertex
	} else {
		// inherit branch root
		branchConeTipVertex.Unwrap(UnwrapOptions{
			Vertex: func(vUnwrap *Vertex) {
				v.StateDelta.baselineBranch = vUnwrap.StateDelta.baselineBranch
			},
		})
		util.Assertf(v.StateDelta.baselineBranch != nil, "v.Branch != nil")
	}
	return nil
}

func (v *Vertex) mustGetBaseState(ut *UTXOTangle) state.SugaredStateReader {
	util.Assertf(!v.Tx.IsSequencerMilestone() || v.StateDelta.baselineBranch != nil, "!v.Tx.IsSequencerMilestone() || v.Branch != nil")
	// determining base state for outputs not on the tangle
	if v.Tx.IsSequencerMilestone() {
		rdr, err := state.NewReadable(ut.stateStore, ut.mustGetBranch(v.StateDelta.baselineBranch).root)
		util.AssertNoError(err)
		return state.MakeSugared(rdr)
	}
	return ut.HeaviestStateForLatestTimeSlot()
}

// FetchMissingDependencies check solidity of inputs and fetches what is available
// Does not obtain global lock on the tangle
// It means in general the result is non-deterministic, because some dependencies may be unavailable. This is ok for solidifier
// Once transaction has all dependencies solid, the result is deterministic
func (v *Vertex) FetchMissingDependencies(ut *UTXOTangle) error {
	var err error
	if v.Tx.IsSequencerMilestone() && v.StateDelta.baselineBranch == nil {
		if err = v.fetchBranchDependency(ut); err != nil {
			return err
		}
		if v.StateDelta.baselineBranch == nil {
			// not solid yet, can't continue with solidification of the sequencer tx
			return nil
		}
	}
	// ---- solidify inputs
	v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		if v.Inputs[i] != nil {
			// it is already solid
			return true
		}
		v.Inputs[i], err = ut.solidifyOutput(oid, func() state.SugaredStateReader {
			return v.mustGetBaseState(ut)
		})
		return err == nil
	})
	if err != nil {
		return err
	}

	//----  solidify endorsements
	v.Tx.ForEachEndorsement(func(i byte, txid *core.TransactionID) bool {
		if v.Endorsements[i] != nil {
			// already solid
			return true
		}
		util.Assertf(v.Tx.TimeSlot() == txid.TimeSlot(), "tx.TimeTick() == txid.TimeTick()")
		if vEnd, solid := ut.GetVertex(txid); solid {
			util.Assertf(vEnd.IsSequencerMilestone(), "vEnd.IsSequencerMilestone()")
			v.Endorsements[i] = vEnd
		}
		return true
	})
	return nil
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
