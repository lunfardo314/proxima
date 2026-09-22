package txbuilder_seq

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/smallkv"
)

// TopUpDelegationRequest adds the whole balance of the request output to the
// delegation it names (kb/delegation_topup.md). A delegation frozen in this
// slot stays frozen with its epoch and pinned share, and the sequencer
// prepays the advance on the added amount over the remaining span; an
// unfrozen one is frozen in the same transition with the amount included.
// There is no fee: the sequencer is paid in the frozen coverage it gains.
type TopUpDelegationRequest struct {
	ledger.TagAlongOutput
	delegation ledger.DelegationOutput
}

const RequestCodeTopUpDelegation = byte(4)

func parseTopUpDelegationOutput(txb *SeqTxBuilder, o *preParsedTagAlongOutput) (cmd TxBuilderCommand, valid bool, reason error) {
	// expected layout: [0] amounts, [1] index-values, [2] tagAlongLock, [3] request data, [4] ensureTopUpDelegation
	ens, idx := o.Output.EnsureTopUpDelegationConstraint()
	if o.Output.NumElements() != 5 || idx != 4 {
		reason = fmt.Errorf("TopUpDelegationRequest: ensureTopUpDelegation expected at element 4")
		return
	}
	if minimum := txb.MinimumTopUp(); o.Output.TokenBalance() < minimum {
		reason = fmt.Errorf("TopUpDelegationRequest: amount %s is below the minimum top-up %s", util.Th(o.Output.TokenBalance()), util.Th(minimum))
		return
	}
	rdr := multistate.MakeSugared(txb.rdr)
	dOut, err := rdr.GetChainOutputWithChainID(ens.ChainID)
	if err != nil {
		reason = fmt.Errorf("TopUpDelegationRequest: failed to retrieve delegation output for %s: '%w'", ens.ChainID.StringShort(), err)
		return
	}
	delegation, ok := ledger.DelegationOutputFromOutputWithChainID(&dOut)
	if !ok {
		reason = fmt.Errorf("TopUpDelegationRequest: %s is not a delegation", ens.ChainID.StringShort())
		return
	}
	if delegation.Target != txb.chainInput.ChainID {
		reason = fmt.Errorf("TopUpDelegationRequest: delegation %s does not target this sequencer", ens.ChainID.StringShort())
		return
	}
	if o.SenderID != delegation.MasterID {
		reason = fmt.Errorf("TopUpDelegationRequest: sender is not the master of delegation %s (authorisation failure)", ens.ChainID.StringShort())
		return
	}
	if delegation.IsInFrozenSlot(txb.Slot()) {
		// a continuation keeps the pinned share; a sequencer that has since
		// raised its cut refuses rather than pays the old share
		if tolerance := 1000 - txb.origSeqData.InflationProfitMarginPromille(); delegation.AdvanceShare > tolerance {
			reason = fmt.Errorf("TopUpDelegationRequest: pinned share %d is above the sequencer's tolerance %d", delegation.AdvanceShare, tolerance)
			return
		}
	} else if _, err = txb.advanceShare(&delegation); err != nil {
		reason = fmt.Errorf("TopUpDelegationRequest: %w", err)
		return
	}
	if !delegation.IsUnlockableByTarget(txb.Slot()) {
		// on hold, inside the safe revocation window or too young: the state
		// may change within the request's window, so it is retried
		valid = true
		reason = fmt.Errorf("TopUpDelegationRequest: delegation %s cannot be unlocked by the target in slot %d", ens.ChainID.StringShort(), txb.Slot())
		return
	}
	return &TopUpDelegationRequest{TagAlongOutput: o.TagAlongOutput, delegation: delegation}, true, nil
}

func (r *TopUpDelegationRequest) Apply(txb *SeqTxBuilder) (valid bool, err error) {
	// the request and the delegation are consumed, the successor produced
	if len(txb.ConsumedOutputs) > 254 {
		return true, fmt.Errorf("TopUpDelegationRequest: too many inputs")
	}
	if len(txb.ProducedOutputs) > 254 {
		return true, fmt.Errorf("TopUpDelegationRequest: too many outputs")
	}
	amount := r.Output.TokenBalance()
	predIdx := byte(len(txb.ConsumedOutputs) + 1)
	var succ *ledger.Output
	if r.delegation.IsInFrozenSlot(txb.Slot()) {
		succ, err = r.delegation.MakeDelegationTopUpOutput(txb.Timestamp(), predIdx, amount)
	} else {
		var share uint16
		if share, err = txb.advanceShare(&r.delegation); err != nil {
			return false, fmt.Errorf("TopUpDelegationRequest: %w", err)
		}
		until, ok := txb.pickFreezeEpoch(r.delegation.Output.TokenBalance() + amount)
		if !ok {
			return true, fmt.Errorf("TopUpDelegationRequest: no epoch to freeze delegation %s into", r.delegation.ChainID.StringShort())
		}
		succ, err = r.delegation.MakeDelegationFreezeOutputWithTopUp(txb.Timestamp(), until, predIdx, share, amount)
	}
	if err != nil {
		return true, fmt.Errorf("TopUpDelegationRequest: %w", err)
	}
	// the request passes straight through to the delegation; what the
	// sequencer pays is the advance on the newly frozen amount
	advance := succ.TokenBalance() - r.delegation.Output.TokenBalance() - succ.Inflation() - amount
	if txb.chainOutAmounts[ledger.AmountIndexTokenBalance] < int64(advance) {
		return true, fmt.Errorf("TopUpDelegationRequest: not enough token balance for advance (%s < %s)",
			util.Th(uint64(txb.chainOutAmounts[ledger.AmountIndexTokenBalance])), util.Th(advance))
	}
	if txb.enforceFreezeUpperBound {
		projected := uint64(txb.chainOutAmounts[ledger.AmountIndexTokenBalance] - int64(advance) +
			txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage] + succ.Amounts().FrozenCoverageAt(0))
		if projected > txb.coverageContributionUpperBound {
			return true, fmt.Errorf("TopUpDelegationRequest: skipping, would exceed coverage contribution upper bound (%s > %s)",
				util.Th(projected), util.Th(txb.coverageContributionUpperBound))
		}
	}

	reqIdx, err := txb.ConsumeOutput(r.Output, r.ID)
	util.AssertNoError(err)
	txb.PutUnlockParams(reqIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

	dIdx, err := txb.ConsumeOutput(r.delegation.Output, r.delegation.ID)
	util.AssertNoError(err)
	util.Assertf(dIdx == predIdx, "dIdx == predIdx")

	succIdx, err := txb.ProduceOutput(succ)
	if err != nil {
		return true, fmt.Errorf("TopUpDelegationRequest: %w", err)
	}
	// the third unlock byte points the delegate lock at the request, whose
	// balance it adds; the request's ensureTopUpDelegation names the successor
	txb.PutUnlockParams(dIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), ledger.DelegationUnlockedByTarget, reqIdx)
	txb.PutUnlockParams(dIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.PutUnlockParams(reqIdx, 4, []byte{succIdx})

	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(advance)
	// the successor's cells are what this transition adds to the chain's
	// vector: the whole balance on a fresh freeze, the increase on a continuation
	for i, c := range succ.Amounts().FrozenCoverageVector(txb.chainMaxFrozenEpochs) {
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage+byte(i)] += c
	}
	return true, nil
}

func (r *TopUpDelegationRequest) Lines(prefix ...string) *lines.Lines {
	return lines.New(prefix...).Add("TopUpDelegationRequest: delegation ID = %s, amount = %s", r.delegation.ChainID.StringShort(), util.Th(r.Output.TokenBalance()))
}

func (r *TopUpDelegationRequest) AttachmentCostDelta() int {
	// +1 for the consumed request, +1 for the delegation input, +1 for the successor
	return 3
}

// NewTopUpDelegationReqOutput builds the top-up request output: a tag-along
// to the delegation's target carrying the amount, the request code at
// element 3 and the ensureTopUpDelegation at element 4.
func NewTopUpDelegationReqOutput(seqID base.ChainID, sender ledger.SigLock, delegationID base.ChainID, amount uint64) *ledger.Output {
	par := smallkv.New()
	par.Set(FieldCmdCode, []byte{RequestCodeTopUpDelegation})
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(amount)
		o.WithLock(&ledger.TagAlongLock{
			TargetSequencerID: seqID,
			SenderID:          base.HolderID(sender),
		})
		o.MustPushConstraint(easyfl.InlineDataBytecode(par.Bytes()))
		o.MustPushConstraint((&ledger.EnsureTopUpDelegation{ChainID: delegationID}).Bytes())
	})
}

// MinimumTopUp is the smallest top-up this sequencer accepts: what it
// declares in its data, never below the ledger-wide floor.
func (txb *SeqTxBuilder) MinimumTopUp() uint64 {
	return max(txb.origSeqData.MinimumTopUp(), txbuildercore.MinimumTopUpAmount)
}
