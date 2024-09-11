package vertex

import (
	"bytes"
	"fmt"
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
	v._addSequencerIndices(br.SequencerOutput.ID.Index(), br.Stem.ID.Index())
	err := v.addOutput(br.SequencerOutput.ID.Index(), br.SequencerOutput.Output)
	util.AssertNoError(err)
	err = v.addOutput(br.Stem.ID.Index(), br.Stem.Output)
	util.AssertNoError(err)
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
		ret._addSequencerIndices(seqIdx, stemIdx)
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

func (v *VirtualTransaction) addOutput(idx byte, o *ledger.Output) error {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	return v._addOutput(idx, o)
}

func (v *VirtualTransaction) _addOutput(idx byte, o *ledger.Output) error {
	oOld, already := v.outputs[idx]
	if already {
		if !bytes.Equal(oOld.Bytes(), o.Bytes()) {
			return fmt.Errorf("VirtualTransaction.addOutput: inconsistent input data at index %d", idx)
		}
		return nil
	}
	v.outputs[idx] = o
	return nil
}

func (v *VirtualTransaction) _addSequencerIndices(seqIdx, stemIdx byte) {
	indices := &[2]byte{seqIdx, stemIdx}
	util.Assertf(seqIdx != 0xff, "seqIdx != 0xff")
	if v.sequencerOutputIndices != nil {
		util.Assertf(*v.sequencerOutputIndices == *indices, "_addSequencerIndices: inconsistent indices")
		return
	}
	v.sequencerOutputIndices = indices
}

func (v *VirtualTransaction) addSequencerOutputs(seqOut, stemOut *ledger.OutputWithID) error {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	seqIdx := seqOut.ID.Index()
	stemIdx := byte(0xff)
	if stemOut != nil {
		stemIdx = stemOut.ID.Index()
	}
	v._addSequencerIndices(seqIdx, stemIdx)

	if err := v._addOutput(seqOut.ID.Index(), seqOut.Output); err != nil {
		return err
	}
	if stemOut != nil {
		if err := v._addOutput(stemOut.ID.Index(), stemOut.Output); err != nil {
			return err
		}
	}
	return nil
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

func (v *VirtualTransaction) sequencerOutputs() (*ledger.Output, *ledger.Output) {
	if v.sequencerOutputIndices == nil {
		return nil, nil
	}
	var seqOut, stemOut *ledger.Output
	var ok bool

	seqOut, ok = v.outputs[v.sequencerOutputIndices[0]]
	util.Assertf(ok, "inconsistency 1 in virtual tx")

	if v.sequencerOutputIndices[1] != 0xff {
		stemOut, ok = v.outputs[v.sequencerOutputIndices[1]]
		util.Assertf(ok, "inconsistency 2 in virtual tx")
	}
	return seqOut, stemOut
}

// SequencerOutputs returns <seq output>, <stem output> or respective nils
func (v *VirtualTransaction) SequencerOutputs() (*ledger.Output, *ledger.Output) {
	v.mutex.RLock()
	defer v.mutex.RUnlock()

	return v.sequencerOutputs()
}

// sequencerID returns nil if not available
func (v *VirtualTransaction) sequencerID(txid *ledger.TransactionID) (ret *ledger.ChainID) {
	if v.sequencerOutputIndices != nil {
		seqOData, ok := v.outputs[v.sequencerOutputIndices[0]].SequencerOutputData()
		util.Assertf(ok, "sequencer output data unavailable for the output #%d", v.sequencerOutputIndices[0])
		idData := seqOData.ChainConstraint.ID
		if idData == ledger.NilChainID {
			oid := ledger.NewOutputID(txid, v.sequencerOutputIndices[0])
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
