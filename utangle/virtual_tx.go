package utangle

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/state"
	"github.com/lunfardo314/proxima/util"
)

func newVirtualTx(txid *core.TransactionID) *VirtualTransaction {
	return &VirtualTransaction{
		txid:    *txid,
		outputs: make(map[byte]*core.Output),
	}
}

func (v *VirtualTransaction) addOutput(idx byte, o *core.Output) {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	_, already := v.outputs[idx]
	util.Assertf(!already, "output %d already present in virtual tx %s", idx, v.txid.Short())
	v.outputs[idx] = o
}

func (v *VirtualTransaction) addSequencerIndices(seqIdx, stemIdx byte) {
	util.Assertf(v.txid.SequencerFlagON(), "addSequencerIndices: must a sequencer transaction")
	util.Assertf(seqIdx != 0xff, "seqIdx != 0xff")
	util.Assertf(v.txid.BranchFlagON() == (stemIdx != 0xff), "v.txid.BranchFlagON() == (stemIdx != 0xff)")
	v.sequencerOutputs = &[2]byte{seqIdx, stemIdx}
}

func (v *VirtualTransaction) Wrap() *WrappedTx {
	return _newVID(_virtualTx{v})
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

func (v *VirtualTransaction) ensureOutputAt(idx byte, stateReader func() state.SugaredStateReader) (*core.Output, error) {
	ret, ok := v.OutputAt(idx)
	if ok {
		return ret, nil
	}

	v.mutex.Lock()
	defer v.mutex.Unlock()

	oid := core.NewOutputID(&v.txid, idx)
	oData, found := stateReader().GetUTXO(&oid)
	if !found {
		return nil, fmt.Errorf("output not found in the state: %s", oid.Short())
	}
	o, err := core.OutputFromBytesReadOnly(oData)
	util.AssertNoError(err)
	v.outputs[idx] = o
	return o, nil
}
