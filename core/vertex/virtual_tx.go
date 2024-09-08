package vertex

import (
	"bytes"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

func newVirtualTx() *VirtualTransaction {
	return &VirtualTransaction{
		Created: time.Now(),
		outputs: make(map[byte]*ledger.Output),
	}
}

func newVirtualBranchTx(br *multistate.BranchData) *VirtualTransaction {
	v := newVirtualTx()
	v.addSequencerIndices(br.SequencerOutput.ID.Index(), br.Stem.ID.Index())
	v.addOutput(br.SequencerOutput.ID.Index(), br.SequencerOutput.Output)
	v.addOutput(br.Stem.ID.Index(), br.Stem.Output)
	v.SetPullNotNeeded()
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

func (v *VirtualTransaction) wrapWithID(txid ledger.TransactionID) *WrappedTx {
	return _newVID(_virtualTx{VirtualTransaction: v}, txid, v.sequencerID(&txid))
}

func WrapBranchDataAsVirtualTx(branchData *multistate.BranchData) *WrappedTx {
	ret := newVirtualBranchTx(branchData).wrapWithID(branchData.Stem.ID.TransactionID())
	cov := branchData.LedgerCoverage
	ret.coverage = &cov
	ret.flags.SetFlagsUp(FlagVertexDefined | FlagVertexTxAttachmentStarted | FlagVertexTxAttachmentFinished)
	return ret
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

// SequencerOutputs returns <seq output>, <stem output> or respective nils
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

// sequencerID returns nil if not available
func (v *VirtualTransaction) sequencerID(txid *ledger.TransactionID) (ret *ledger.ChainID) {
	if v.sequencerOutputs != nil {
		seqOData, ok := v.outputs[v.sequencerOutputs[0]].SequencerOutputData()
		util.Assertf(ok, "sequencer output data unavailable for the output #%d", v.sequencerOutputs[0])
		idData := seqOData.ChainConstraint.ID
		if idData == ledger.NilChainID {
			oid := ledger.NewOutputID(txid, v.sequencerOutputs[0])
			ret = util.Ref(ledger.MakeOriginChainID(&oid))
		} else {
			ret = util.Ref(idData)
		}
	}
	return
}

// functions to manipulate pull information in the virtual transaction
// Real transactions (full vertices) do not need pull

func (v *VirtualTransaction) PullRulesDefined() bool {
	return v.pullRulesDefined
}

func (v *VirtualTransaction) SetPullDeadline(deadline time.Time) {
	v.pullRulesDefined = true
	v.needsPull = true
	v.pullDeadline = deadline
}

func (v *VirtualTransaction) SetPullNotNeeded() {
	v.pullRulesDefined = true
	v.needsPull = false
}

func (v *VirtualTransaction) SetLastPullNow() {
	util.Assertf(v.pullRulesDefined, "v.pullRulesDefined")
	v.lastPull = time.Now()
}

func (v *VirtualTransaction) PullDeadlineExpired() bool {
	return v.pullRulesDefined && v.needsPull && time.Now().After(v.pullDeadline)
}

func (v *VirtualTransaction) PullNeeded(repeatPeriod time.Duration) bool {
	return v.pullRulesDefined && v.needsPull && v.lastPull.Add(repeatPeriod).Before(time.Now())
}
