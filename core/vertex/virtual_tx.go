package vertex

import (
	"bytes"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
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
	err := v._addSequencerIndices(br.SequencerOutput.ID.Index(), br.Stem.ID.Index())
	util.AssertNoError(err)
	v.mustAddOutput(br.SequencerOutput.ID.Index(), br.SequencerOutput.Output)
	v.mustAddOutput(br.Stem.ID.Index(), br.Stem.Output)
	v.pullRulesDefined = true
	v.needsPull = false
	return v
}

// toDetachedVertex preserves information about all outputs and baseline in the virtualTx
func (v *Vertex) toDetachedVertex() *DetachedVertex {
	ret := &DetachedVertex{Transaction: v.Transaction}
	ret.BranchID = v.BaselineBranchID
	return ret
}

func (v *VirtualTransaction) wrapWithID(txid base.TransactionID) *WrappedTx {
	return _newVID(_virtualTx{VirtualTransaction: v}, txid, v.sequencerID(txid))
}

// WrapBranchDataAsVirtualTx branch vertex immediately becomes 'good'
func WrapBranchDataAsVirtualTx(branchData *multistate.BranchData) *WrappedTx {
	ret := newVirtualBranchTx(branchData).wrapWithID(branchData.Stem.ID.TransactionID())
	ret.flags.SetFlagsUp(FlagVertexDefined | FlagVertexTxAttachmentStarted | FlagVertexTxAttachmentFinished)
	return ret
}

func (v *VirtualTransaction) mustAddOutput(idx byte, o *ledger.Output) {
	v.mutex.Lock()
	defer v.mutex.Unlock()

	v._mustAddOutput(idx, o)
}

func (v *VirtualTransaction) _mustAddOutput(idx byte, o *ledger.Output) {
	oOld, already := v.outputs[idx]
	if already {
		util.Assertf(bytes.Equal(oOld.Bytes(), o.Bytes()), "VirtualTransaction.mustAddOutput: inconsistent input data at index %d", idx)
	}
	v.outputs[idx] = o.Clone()
}

func (v *VirtualTransaction) _addSequencerIndices(seqIdx, stemIdx byte) error {
	indices := &[2]byte{seqIdx, stemIdx}
	util.Assertf(seqIdx != 0xff, "seqIdx != 0xff")
	if v.sequencerOutputIndices != nil && *v.sequencerOutputIndices != *indices {
		return fmt.Errorf("_addSequencerIndices: inconsistent indices: expected (%d,%d), got (%d,%d)",
			seqIdx, stemIdx, (*v.sequencerOutputIndices)[0], (*v.sequencerOutputIndices)[1])
	}
	v.sequencerOutputIndices = indices
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
func (v *VirtualTransaction) sequencerID(txid base.TransactionID) (ret *base.ChainID) {
	if v.sequencerOutputIndices != nil {
		seqOData, ok := v.outputs[v.sequencerOutputIndices[0]].SequencerOutputData()
		util.Assertf(ok, "sequencer output data unavailable for the output #%d", v.sequencerOutputIndices[0])
		idData := seqOData.ChainConstraint.ChainID
		if idData == base.NilChainID {
			oid := base.MustNewOutputID(txid, v.sequencerOutputIndices[0])
			ret = util.Ref(base.MakeOriginChainID(oid))
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

func (v *VirtualTransaction) SetPullNeeded() {
	v.pullRulesDefined = true
	v.needsPull = true
	v.timesPulled = 0
	v.nextPull = time.Now().Add(-time.Nanosecond) // slightly in the past to ensure PullNeeded() returns true immediately
}

// SetPullHappened increases pull counter and sets nex pull deadline
func (v *VirtualTransaction) SetPullHappened(repeatAfter time.Duration) {
	util.Assertf(v.pullRulesDefined, "v.pullRulesDefined")
	v.timesPulled++
	v.nextPull = time.Now().Add(repeatAfter)
}

func (v *VirtualTransaction) PullPatienceExpired(maxPullAttempts int, isDepthCapped func() bool) bool {
	return v.PullNeeded(isDepthCapped) && v.timesPulled >= maxPullAttempts
}

// MaxAttachmentDepthForPull is the depth cap for gossip-driven recursive pull,
// counted in BRANCHES along the backward walk — lineage distance, roughly "how
// many slots behind" (see claude/sync_semantics.md §2.1, §2). A node at the tip
// has depth ~1 and never caps; only a node genuinely many branches behind reaches
// the cap, where it stops pulling and polls until the dependency becomes rooted.
//
// The cap is a PURE CONSTANT given the configuration: this value applies when
// forward sync is enabled (it advances committed state, so recursion stays close
// to the frontier); MaxAttachmentDepthForPullNoForwardSync applies when forward
// sync is off (recursion is the only mechanism and must reach the whole gap from
// the local txstore). The attacher reads the effective value opaquely via
// Environment.AttachmentDepthCap() and knows nothing about forward sync.
const MaxAttachmentDepthForPull = 50

// MaxAttachmentDepthForPullNoForwardSync is the depth cap used when forward sync
// is disabled. Moderate — large enough that recursive sync alone bridges a typical
// short outage from the local txstore, but NOT so large that a very-far-behind node
// thrashes (a 1000-branch recursion spawned thousands of attachers / goroutines and
// exhausted memory, 2026-06-20). Beyond it the node is too far behind for recursion
// alone and the sync-orchestration layer must refuse / seek a newer snapshot
// (sync_semantics.md §4) — that orchestration is NOT yet implemented.
const MaxAttachmentDepthForPullNoForwardSync = 500

// PullNeeded returns true if pulling is needed and allowed.
// isDepthCapped closure is provided by the caller — it captures attachment depth
// to decide whether depth-capping applies.
func (v *VirtualTransaction) PullNeeded(isDepthCapped func() bool) bool {
	return !isDepthCapped() && v.pullRulesDefined && v.needsPull && v.nextPull.Before(time.Now())
}

func (v *VirtualTransaction) findChainOutput(txid base.TransactionID, chainID *base.ChainID) *ledger.OutputWithID {
	v.mutex.RLock()
	defer v.mutex.RUnlock()

	for outIdx, o := range v.outputs {
		if c := o.ChainConstraint(); c != nil && c.ChainID == *chainID {
			return &ledger.OutputWithID{
				ID:     base.MustNewOutputID(txid, outIdx),
				Output: o,
			}
		}
	}
	return nil
}
