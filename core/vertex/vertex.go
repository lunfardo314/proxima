package vertex

import (
	"fmt"
	"strings"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

func NewVertex(tx *transaction.Transaction) *Vertex {
	return &Vertex{
		Transaction:  tx,
		Inputs:       make([]*WrappedTx, tx.NumInputs()),
		Endorsements: make([]*WrappedTx, tx.NumEndorsements()),
	}
}

func (v *Vertex) ReferenceInput(i byte, vid *WrappedTx) {
	util.Assertf(int(i) < len(v.Inputs), "ReferenceInput: wrong input index")
	util.Assertf(v.Inputs[i] == nil, "ReferenceInput: repetitive")

	v.Inputs[i] = vid
}

func (v *Vertex) ReferenceEndorsement(i byte, vid *WrappedTx) {
	util.Assertf(int(i) < len(v.Endorsements), "ReferenceEndorsement: wrong endorsement index")
	util.Assertf(v.Endorsements[i] == nil, "ReferenceEndorsement: repetitive")

	v.Endorsements[i] = vid
}

// UnReferenceDependencies un-references all not nil inputs and endorsements and invalidates vertex structure
// TODO revisit usages
func (v *Vertex) UnReferenceDependencies() {
	clear(v.Inputs)
	clear(v.Endorsements)
}

// InputLoaderByIndex returns raw bytes of the consumed output at index i,
// or an error if the input is orphaned or inaccessible in the virtualTx.
func (v *Vertex) InputLoaderByIndex(i byte) ([]byte, error) {
	o := v.GetConsumedOutput(i)
	if o == nil {
		oid := v.MustInputAt(i)
		return nil, fmt.Errorf("InputLoaderByIndex: consumed output %s at index %d is not available", oid.StringShort(), i)
	}
	return o.Bytes(), nil
}

// GetConsumedOutput return produced output, is available. Returns nil if unavailable for any reason
func (v *Vertex) GetConsumedOutput(i byte) (ret *ledger.Output) {
	if int(i) >= len(v.Inputs) || v.Inputs[i] == nil {
		return
	}
	idx := v.MustOutputIndexOfTheInput(i)
	v.Inputs[i].RUnwrap(UnwrapOptions{
		Vertex: func(vCons *Vertex) {
			ret = vCons.MustProducedOutputAt(idx)
		},
		DetachedVertex: func(vCons *DetachedVertex) {
			ret = vCons.MustProducedOutputAt(idx)
		},
		VirtualTx: func(vCons *VirtualTransaction) {
			ret, _ = vCons.OutputAt(idx)
		},
	})
	return
}

// ValidateConstraints creates full transaction context from the (solid) vertex data
// and runs validation of all constraints in the context
func (v *Vertex) ValidateConstraints() error {
	err := v.Transaction.SetFullContext(v.InputLoaderByIndex)
	if err != nil {
		return fmt.Errorf("ValidateConstraints of %s: %w", v.IDShortString(), err)
	}
	err = v.ValidateFullContext()

	const validateConstraintsVerbose = true

	if err != nil {
		if validateConstraintsVerbose {
			err = fmt.Errorf("ValidateConstraints: %w \n>>>>>>>>>>>>>>>>>>>>>\n%s", err, v.String())
		} else {
			err = fmt.Errorf("ValidateConstraints: %s: %w", v.IDShortString(), err)
		}
		return err
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
func (v *Vertex) MissingInputTxIDSet() set.Set[base.TransactionID] {
	ret := set.New[base.TransactionID]()
	var oid base.OutputID
	v.ForEachInputDependency(func(i byte, vidInput *WrappedTx) bool {
		if vidInput == nil {
			oid = v.MustInputAt(i)
			ret.Insert(oid.TransactionID())
		}
		return true
	})
	v.ForEachEndorsement(func(i byte, vidEndorsed *WrappedTx) bool {
		if vidEndorsed == nil {
			ret.Insert(v.MustEndorsementAt(i))
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
	ret := make([]string, 0, len(s))
	for txid := range s {
		ret = append(ret, txid.StringShort())
	}
	return strings.Join(ret, ", ")
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

func (v *Vertex) SetOfInputTransactions() set.Set[*WrappedTx] {
	ret := set.New[*WrappedTx]()
	v.ForEachInputDependency(func(_ byte, vidInput *WrappedTx) bool {
		ret.Insert(vidInput)
		return true
	})
	return ret
}

func (v *Vertex) Lines(prefix ...string) *lines.Lines {
	return v.Transaction.Lines(func(i byte) ([]byte, error) {
		if v.Inputs[i] == nil {
			return nil, fmt.Errorf("input #%d not solid", i)
		}
		inpOid, err := v.InputAt(i)
		if err != nil {
			return nil, fmt.Errorf("input #%d: %v", i, err)
		}
		o, err := v.Inputs[i].OutputAt(inpOid.Index())
		if err != nil {
			return nil, err
		}
		return o.Bytes(), nil
	}, prefix...)
}

func (v *DetachedVertex) Lines(prefix ...string) *lines.Lines {
	return v.LinesShort(prefix...)
}

func (v *Vertex) Wrap() *WrappedTx {
	var seqID *base.ChainID
	if v.IsSequencerTransaction() {
		seqID = util.Ref(v.SequencerTransactionData().SequencerID)
	}
	return _newVID(_vertex{Vertex: v}, v.ID(), seqID)
}
