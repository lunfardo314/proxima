package vertex

import (
	"bytes"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

func newVirtualTx(txid core.TransactionID) *VirtualTransaction {
	return &VirtualTransaction{
		txid:    txid,
		outputs: make(map[byte]*core.Output),
	}
}

func NewVirtualBranchTx(br *multistate.BranchData) *VirtualTransaction {
	txid := br.Stem.ID.TransactionID()
	v := newVirtualTx(txid)
	v.addSequencerIndices(br.SequencerOutput.ID.Index(), br.Stem.ID.Index())
	v.addOutput(br.SequencerOutput.ID.Index(), br.SequencerOutput.Output)
	v.addOutput(br.Stem.ID.Index(), br.Stem.Output)
	return v
}

func (v *VirtualTransaction) Wrap() *WrappedTx {
	return _newVID(_virtualTx{
		VirtualTransaction: v},
		v.txid,
	)
}

func (v *VirtualTransaction) addOutput(idx byte, o *core.Output) {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	oOld, already := v.outputs[idx]
	if already {
		util.Assertf(bytes.Equal(oOld.Bytes(), o.Bytes()), "addOutput: inconsistent output %d data in virtual tx %s", idx, v.txid.StringShort())
		return
	}
	v.outputs[idx] = o
}

func (v *VirtualTransaction) addSequencerIndices(seqIdx, stemIdx byte) {
	util.Assertf(v.txid.IsSequencerMilestone(), "addSequencerIndices: must a sequencer transaction")
	util.Assertf(seqIdx != 0xff, "seqIdx != 0xff")
	util.Assertf(v.txid.IsBranchTransaction() == (stemIdx != 0xff), "v.txid.IsBranchTransaction() == (stemIdx != 0xff)")
	v.sequencerOutputs = &[2]byte{seqIdx, stemIdx}
}

func (v *VirtualTransaction) outputWithIDAt(idx byte) (*core.OutputWithID, bool) {
	ret, ok := v.OutputAt(idx)
	if !ok {
		return nil, false
	}
	return &core.OutputWithID{
		ID:     core.NewOutputID(&v.txid, idx),
		Output: ret,
	}, true
}

func (v *VirtualTransaction) OutputAt(idx byte) (*core.Output, bool) {
	v.mutex.RLock()
	defer v.mutex.RUnlock()

	if o, isFetched := v.outputs[idx]; isFetched {
		return o, true
	}
	return nil, false
}

// SequencerOutputs returns <seq output>, <branch output> or respective nils
func (v *VirtualTransaction) SequencerOutputs() (*core.Output, *core.Output) {
	v.mutex.RLock()
	defer v.mutex.RUnlock()

	if v.sequencerOutputs == nil {
		return nil, nil
	}
	var seqOut, stemOut *core.Output
	var ok bool

	seqOut, ok = v.outputs[v.sequencerOutputs[0]]
	util.Assertf(ok, "inconsistency 1 in virtual tx %s", v.txid.StringShort())

	if v.sequencerOutputs[1] != 0xff {
		stemOut, ok = v.outputs[v.sequencerOutputs[1]]
		util.Assertf(ok, "inconsistency 2 in virtual tx %s", v.txid.StringShort())
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
