package vertex

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

func New(tx *transaction.Transaction) *Vertex {
	ret := &Vertex{
		Tx:           tx,
		Inputs:       make([]*WrappedTx, tx.NumInputs()),
		Endorsements: make([]*WrappedTx, tx.NumEndorsements()),
	}
	return ret
}

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

func (v *Vertex) ValidateConstraints(traceOption ...int) error {
	if v.constraintsValid {
		return nil
	}
	traceOpt := transaction.TraceOptionFailedConstraints
	if len(traceOption) > 0 {
		traceOpt = traceOption[0]
	}
	ctx, err := transaction.ContextFromTransaction(v.Tx, v.getConsumedOutput, traceOpt)
	if err != nil {
		return err
	}
	err = ctx.Validate()
	if err == nil {
		v.constraintsValid = true
	}
	return err
}

func (v *Vertex) ValidateDebug() (string, error) {
	ctx, err := transaction.ContextFromTransaction(v.Tx, v.getConsumedOutput)
	if err != nil {
		return "", err
	}
	return ctx.String(), ctx.Validate()
}

func (v *Vertex) NumMissingInputs() (missingInputs int, missingEndorsements int) {
	v.ForEachInputDependency(func(_ byte, vidInput *WrappedTx) bool {
		if vidInput == nil {
			missingInputs++
		}
		return true
	})
	v.ForEachEndorsement(func(_ byte, vidEndorsed *WrappedTx) bool {
		if vidEndorsed == nil {
			missingEndorsements++
		}
		return true
	})
	return
}

// MissingInputTxIDSet returns set of txids for the missing inputs and endorsements
func (v *Vertex) MissingInputTxIDSet() set.Set[core.TransactionID] {
	ret := set.New[core.TransactionID]()
	var oid core.OutputID
	v.ForEachInputDependency(func(i byte, vidInput *WrappedTx) bool {
		if vidInput == nil {
			oid = v.Tx.MustInputAt(i)
			ret.Insert(oid.TransactionID())
		}
		return true
	})
	v.ForEachEndorsement(func(i byte, vidEndorsed *WrappedTx) bool {
		if vidEndorsed == nil {
			ret.Insert(v.Tx.EndorsementAt(i))
		}
		return true
	})
	return ret
}

func (v *Vertex) MissingInputTxIDString() string {
	s := v.MissingInputTxIDSet()
	if len(s) == 0 {
		return "(none)"
	}
	ret := make([]string, 0)
	for txid := range s {
		ret = append(ret, txid.StringShort())
	}
	return strings.Join(ret, ", ")
}

func (v *Vertex) StemInputIndex() byte {
	util.Assertf(v.Tx.IsBranchTransaction(), "branch vertex expected")

	predOID := v.Tx.StemOutputData().PredecessorOutputID
	var stemInputIdx byte
	var stemInputFound bool

	v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		if *oid == predOID {
			stemInputIdx = i
			stemInputFound = true
		}
		return !stemInputFound
	})
	util.Assertf(stemInputFound, "can't find stem input")
	return stemInputIdx
}

func (v *Vertex) SequencerInputIndex() byte {
	util.Assertf(v.Tx.IsSequencerMilestone(), "sequencer milestone expected")
	return v.Tx.SequencerTransactionData().SequencerOutputData.ChainConstraint.PredecessorInputIndex
}

func (v *Vertex) _allInputsSolid() bool {
	for _, d := range v.Inputs {
		if d == nil {
			return false
		}
	}
	return true
}

func (v *Vertex) _allEndorsementsSolid() bool {
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

func (v *Vertex) ForEachInputDependency(fun func(i byte, vidInput *WrappedTx) bool) {
	for i, inp := range v.Inputs {
		if !fun(byte(i), inp) {
			return
		}
	}
}

func (v *Vertex) ForEachEndorsement(fun func(i byte, vidEndorsed *WrappedTx) bool) {
	for i, vEnd := range v.Endorsements {
		if !fun(byte(i), vEnd) {
			return
		}
	}
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
	v.ForEachInputDependency(func(i byte, inp *WrappedTx) bool {
		if inp == nil {
			oid := v.Tx.MustInputAt(i)
			ret.Add("   %d : %s", i, oid.StringShort())
		}
		return true
	})
	ret.Add("not solid endorsements:")
	v.ForEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
		if vEnd == nil {
			txid := v.Tx.EndorsementAt(i)
			ret.Add("   %d : %s", i, txid.StringShort())
		}
		return true
	})
	return ret
}
