package vertex

import (
	"bytes"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

func newVirtualTx() *VirtualTransaction {
	return &VirtualTransaction{
		outputs: make(map[byte]*ledger.Output),
	}
}

func NewVirtualBranchTx(br *multistate.BranchData) *VirtualTransaction {
	v := newVirtualTx()
	v.addSequencerIndices(br.SequencerOutput.ID.Index(), br.Stem.ID.Index())
	v.addOutput(br.SequencerOutput.ID.Index(), br.SequencerOutput.Output)
	v.addOutput(br.Stem.ID.Index(), br.Stem.Output)
	return v
}

// VirtualTxFromTx converts transaction to a collection of produced outputs
func VirtualTxFromTx(tx *transaction.Transaction) *VirtualTransaction {
	ret := &VirtualTransaction{
		outputs: make(map[byte]*ledger.Output),
	}
	for i := 0; i < tx.NumProducedOutputs(); i++ {
		ret.outputs[byte(i)] = tx.MustProducedOutputAt(byte(i))
	}
	if tx.IsSequencerMilestone() {
		seqIdx, stemIdx := tx.SequencerAndStemOutputIndices()
		ret.addSequencerIndices(seqIdx, stemIdx)
	}
	return ret
}

func (v *VirtualTransaction) WrapWithID(txid ledger.TransactionID) *WrappedTx {
	return _newVID(_virtualTx{VirtualTransaction: v}, txid)
}

func (v *VirtualTransaction) addOutput(idx byte, o *ledger.Output) {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	oOld, already := v.outputs[idx]
	if already {
		util.Assertf(bytes.Equal(oOld.Bytes(), o.Bytes()), "addOutput: inconsistent output %d data in virtual tx", idx)
		return
	}
	v.outputs[idx] = o
}

func (v *VirtualTransaction) addSequencerIndices(seqIdx, stemIdx byte) {
	util.Assertf(seqIdx != 0xff, "seqIdx != 0xff")
	v.sequencerOutputs = &[2]byte{seqIdx, stemIdx}
}

// OutputAt return output at the index and true, or nil, false if output is not available in the virtual tx
func (v *VirtualTransaction) OutputAt(idx byte) (*ledger.Output, bool) {
	v.mutex.RLock()
	defer v.mutex.RUnlock()

	if o, isAvailable := v.outputs[idx]; isAvailable {
		return o, true
	}
	return nil, false
}

// SequencerOutputs returns <seq output>, <branch output> or respective nils
func (v *VirtualTransaction) SequencerOutputs() (*ledger.Output, *ledger.Output) {
	v.mutex.RLock()
	defer v.mutex.RUnlock()

	if v.sequencerOutputs == nil {
		return nil, nil
	}
	var seqOut, stemOut *ledger.Output
	var ok bool

	seqOut, ok = v.outputs[v.sequencerOutputs[0]]
	util.Assertf(ok, "inconsistency 1 in virtual tx")

	if v.sequencerOutputs[1] != 0xff {
		stemOut, ok = v.outputs[v.sequencerOutputs[1]]
		util.Assertf(ok, "inconsistency 2 in virtual tx")
	}
	return seqOut, stemOut
}

func (v *VirtualTransaction) mustMergeNewOutputs(vNew *VirtualTransaction) {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	if v.sequencerOutputs != nil && vNew.sequencerOutputs != nil {
		util.Assertf(*v.sequencerOutputs == *vNew.sequencerOutputs, "mustMergeNewOutputs: inconsistent sequencer output data")
	}
	if v.sequencerOutputs == nil {
		v.sequencerOutputs = vNew.sequencerOutputs
	}
	for idx, o := range vNew.outputs {
		if oOld, already := v.outputs[idx]; already {
			util.Assertf(bytes.Equal(o.Bytes(), oOld.Bytes()), "mustMergeNewOutputs: inconsistent output data")
		} else {
			v.outputs[idx] = o
		}
	}
}
