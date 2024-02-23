package vertex

import (
	"fmt"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
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

func (v *Vertex) TimeSlot() ledger.Slot {
	return v.Tx.ID().Slot()
}

func (v *Vertex) getSequencerPredecessor() *WrappedTx {
	util.Assertf(v.Tx.IsSequencerMilestone(), "v.Tx.IsSequencerMilestone()")
	predIdx := v.Tx.SequencerTransactionData().SequencerOutputData.ChainConstraint.PredecessorInputIndex
	return v.Inputs[predIdx]
}

// ReferenceInput puts new input and references it. In case referencing fails, no change and return false
func (v *Vertex) ReferenceInput(i byte, vid *WrappedTx) bool {
	util.Assertf(int(i) < len(v.Inputs), "PutNewInput: wrong input index")
	util.Assertf(v.Inputs[i] == nil, "PutNewInput: repetitive")
	referenced := vid.Reference("ReferenceInput")
	if referenced {
		v.Inputs[i] = vid
	}
	return referenced
}

func (v *Vertex) ReferenceEndorsement(i byte, vid *WrappedTx) bool {
	util.Assertf(int(i) < len(v.Endorsements), "PutNewEndorsement: wrong endorsement index")
	util.Assertf(v.Endorsements[i] == nil, "PutNewEndorsement: repetitive")
	referenced := vid.Reference("ReferenceEndorsement")
	if referenced {
		v.Endorsements[i] = vid
	}
	return referenced
}

// UnReferenceDependencies un-references all not nil inputs
func (v *Vertex) UnReferenceDependencies() {
	for i, vidInput := range v.Inputs {
		if vidInput != nil {
			vidInput.UnReference("UnReferenceDependencies 1")
			v.Inputs[i] = nil
		}
	}
	if v.BaselineBranch != nil {
		v.BaselineBranch.UnReference("UnReferenceDependencies 2")
		v.BaselineBranch = nil
	}
	for i, vidEndorsement := range v.Endorsements {
		if vidEndorsement != nil {
			vidEndorsement.UnReference("UnReferenceDependencies 3")
			v.Endorsements[i] = nil
		}
	}
}

// InputLoaderByIndex returns consumed output at index i or nil (if input is orphaned or inaccessible in the virtualTx)
func (v *Vertex) InputLoaderByIndex(i byte) (*ledger.Output, error) {
	o := v.GetConsumedOutput(i)
	if o == nil {
		return nil, fmt.Errorf("consumed output at index %d is not available", i)
	}
	return o, nil
}

// GetConsumedOutput return produced output, is available. Returns nil if unavailable for any reason
func (v *Vertex) GetConsumedOutput(i byte) (ret *ledger.Output) {
	if int(i) >= len(v.Inputs) || v.Inputs[i] == nil {
		return
	}
	v.Inputs[i].RUnwrap(UnwrapOptions{
		Vertex: func(vCons *Vertex) {
			ret = vCons.Tx.MustProducedOutputAt(v.Tx.MustOutputIndexOfTheInput(i))
		},
		VirtualTx: func(vCons *VirtualTransaction) {
			ret, _ = vCons.OutputAt(v.Tx.MustOutputIndexOfTheInput(i))
		},
	})
	return
}

func (v *Vertex) ValidateConstraints(traceOption ...int) error {
	traceOpt := transaction.TraceOptionFailedConstraints
	if len(traceOption) > 0 {
		traceOpt = traceOption[0]
	}
	ctx, err := transaction.TxContextFromTransaction(v.Tx, v.InputLoaderByIndex, traceOpt)
	if err != nil {
		return err
	}
	err = ctx.Validate()
	if err != nil {
		return fmt.Errorf("ValidateConstraints: %s: %w", v.Tx.IDShortString(), err)
	}
	return nil
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
func (v *Vertex) MissingInputTxIDSet() set.Set[ledger.TransactionID] {
	ret := set.New[ledger.TransactionID]()
	var oid ledger.OutputID
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

	v.Tx.ForEachInput(func(i byte, oid *ledger.OutputID) bool {
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

func (v *Vertex) MustProducedOutput(idx byte) (*ledger.Output, bool) {
	odata, ok := v.producedOutputData(idx)
	if !ok {
		return nil, false
	}
	o, err := ledger.OutputFromBytesReadOnly(odata)
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
	return v.Tx.Lines(func(i byte) (*ledger.Output, error) {
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
	}, *v.Tx.ID())
}

func (v *Vertex) convertToVirtualTx() *VirtualTransaction {
	ret := &VirtualTransaction{
		outputs: make(map[byte]*ledger.Output, v.Tx.NumProducedOutputs()),
	}
	if v.Tx.IsSequencerMilestone() {
		seqIdx, stemIdx := v.Tx.SequencerAndStemOutputIndices()
		ret.sequencerOutputs = &[2]byte{seqIdx, stemIdx}
	}

	v.Tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, _ *ledger.OutputID) bool {
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
