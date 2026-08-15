package txbuilder_seq

import (
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type AskStopDelegationRequest struct {
	ledger.TagAlongOutput
	delegationID base.ChainID
	delegation   ledger.DelegationOutput
	// takeFromDelegation is the compensation charged to the delegation balance
	// rather than to the request output. Equals the allowance the delegator
	// authorised; 0 when there is none.
	takeFromDelegation   uint64
	ensureStopDelegation *ledger.EnsureStopDelegation
}

const (
	RequestCodeAskStopDelegation = byte(3)

	FieldRevokeDelegationID = 'i'
)

func parseAskStopDelegationOutput(txb *SeqTxBuilder, o *preParsedTagAlongOutput) (cmd TxBuilderCommand, valid bool, reason error) {
	delegationID, reason := base.ChainIDFromBytes(o.RequestParams.Get(FieldRevokeDelegationID))
	if reason != nil {
		reason = fmt.Errorf("AskStopDelegationRequest: parse failed: %w", reason)
		return
	}
	ret := &AskStopDelegationRequest{
		TagAlongOutput: o.TagAlongOutput,
		delegationID:   delegationID,
	}
	// ---------- fetch delegation output from the baseline state
	rdr := multistate.MakeSugared(txb.rdr)
	_dOut, reason := rdr.GetChainOutputWithChainID(delegationID)
	if reason != nil {
		// wrong chain ID
		reason = fmt.Errorf("AskStopDelegationRequest: failed to retrieve delegation output for %s: '%w'", delegationID.StringShort(), reason)
		return
	}

	var ok bool
	ret.delegation, ok = ledger.DelegationOutputFromOutputWithChainID(&_dOut)
	if !ok {
		// is not a valid delegation chain output
		reason = fmt.Errorf("AskStopDelegationRequest: failed to parse delegation output %s: %w", delegationID.StringShort(), reason)
		return
	}
	// ----------

	// ---------- check if revocation even makes sense
	if !ret.delegation.IsInFrozenSlot(txb.Slot()) {
		// permanently invalid because later this particular revoke request won't make sense anyway
		reason = fmt.Errorf("AskStopDelegationRequest: delegation is not frozen in the slot %d", txb.Slot())
		return
	}

	// ---------- authenticate: check if the sender of the request and the sequencer must be entitled to revoke particular delegation ID
	if ret.delegation.Target != txb.chainInput.ChainID {
		// this sequencer cannot revoke specific delegation
		reason = fmt.Errorf("AskStopDelegationRequest: the sequencer cannot revoke delegation %s (failed authorisation)", delegationID.String())
		return
	}
	// check authorisation
	if o.SenderID != ret.delegation.MasterID {
		// this sender cannot revoke delegation -> may be an attack
		reason = fmt.Errorf("AskStopDelegationRequest: sender with hash %s cannot revoke delegation %s (authorisation failure)",
			hex.EncodeToString(o.SenderID[:]), delegationID.String())
		return
	}

	//------------

	// ------------ check if revocation makes economic sense for the sequencer:
	// tokens provided in the tag-along output must at least cover the remaining projected inflation from the frozen amount
	unfreezeSlot := ret.delegation.UnfreezeSlot()
	util.Assertf(unfreezeSlot > txb.Slot(), "unfreezeSlot > txb.Slot()")

	const patienceMargin = 6
	// fix: was txb.Slot() - unfreezeSlot, uint32 underflow because unfreezeSlot > txb.Slot() (asserted above)
	lostSlots := unfreezeSlot - txb.Slot()
	if lostSlots <= patienceMargin {
		// less than 1 min slots until the end of the freeze, refuse to revoke.
		// Just 1 min of patience, and it will be released to the safe revocation window without revocation command
		reason = fmt.Errorf("AskStopDelegationRequest: less than %d slots remain until safe revocation window. Wait a bit", patienceMargin)
		return
	}
	// check if 'ensureStopDelegation' constraint exists, if yes, sequencer will need to unlock it
	if ens, idx := o.Output.EnsureStopDelegationConstraint(); idx != 0xff {
		// expected layout: [0] amounts, [1] index-values, [2] tagAlongLock, [3] request data, [4] ensureStopDelegation.
		if idx != 4 || ens.ChainID != delegationID {
			// wrong structure. Ensure revocation constraint expected at index 4
			// fix: bare return left cmd=nil, reason=nil -> nil pointer dereference in AddTagAlongInput
			reason = fmt.Errorf("AskStopDelegationRequest: wrong ensureStopDelegation constraint (idx=%d)", idx)
			return
		}
		ret.ensureStopDelegation = ens
		// The allowance authorises taking compensation out of the delegation
		// balance. Reject anything above what the constraint itself will
		// accept, rather than building a transaction that cannot validate.
		if ceiling := ret.delegation.AllowanceCeiling(); ens.Allowance > ceiling {
			reason = fmt.Errorf("AskStopDelegationRequest: allowance %s exceeds the ceiling %s for delegation %s",
				util.Th(ens.Allowance), util.Th(ceiling), delegationID.StringShort())
			return
		}
		ret.takeFromDelegation = ens.Allowance
	}

	// Stopping early is an unwind: what the sequencer needs back is the part of
	// the advance the remaining span will no longer earn, at the share it
	// actually advanced (pinned in delegateLockState at freeze time). It is not
	// entitled to its foregone cut on top - both sides bear their own foregone
	// inflation. Mirrors _projectedCompensation in ensure.easyfl.
	projected := txb.Library.ChainInflationMultiStep(ret.delegation.Output.TokenBalance(), txb.Slot(), lostSlots)
	neededCompensation := (projected * uint64(ret.delegation.AdvanceShare)) / 1000
	if neededCompensation > o.Output.TokenBalance()+ret.takeFromDelegation {
		// projected inflation advance is bigger than what the request output plus the
		// allowance can cover -> sequencer do not want loss -> ignore the revocation request
		// fix: bare return left cmd=nil, reason=nil -> nil pointer dereference in AddTagAlongInput
		reason = fmt.Errorf("AskStopDelegationRequest: compensation not sufficient (needed %d, provided %d + allowance %d)",
			neededCompensation, o.Output.TokenBalance(), ret.takeFromDelegation)
		return
	}
	return ret, true, nil
}

func (r *AskStopDelegationRequest) Apply(txb *SeqTxBuilder) (valid bool, err error) {
	// need to reserve at least 2 outputs
	if len(txb.ConsumedOutputs) > 254 {
		return true, fmt.Errorf("AskStopDelegationRequest: too many outputs to consume")
	}
	if len(txb.ProducedOutputs) > 255 {
		return true, fmt.Errorf("AskStopDelegationRequest: too many outputs to produce")
	}
	inflation := txb.Library.ChainInflationOneSlot(r.delegation.Output.TokenBalance(), r.delegation.ID.Slot())

	oProduce, err := r.delegation.MakeDelegationRevokeOutput(ledger.MakeDelegationRevokeOutputParams{
		TxTs:             txb.Timestamp(),
		PredOutputIndex:  byte(len(txb.ConsumedOutputs) + 1),
		Inflation:        inflation,
		HarvestInflation: inflation, // take last inflation bit from delegation
		TakeFromBalance:  r.takeFromDelegation,
	})
	if err != nil {
		return true, fmt.Errorf("AskStopDelegationRequest: %w", err)
	}

	// consume tag-along with the revoke command message
	tagAlongOutputIdx, err := txb.ConsumeOutput(r.Output, r.ID)
	util.AssertNoError(err)
	txb.PutUnlockParams(tagAlongOutputIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))

	// consume the delegation predecessor
	predIdx, err := txb.ConsumeOutput(r.delegation.Output, r.delegation.ID)
	util.AssertNoError(err)

	// produce revokeRequests delegation output
	revocationOutputIndex, err := txb.ProduceOutput(oProduce)
	if err != nil {
		return false, fmt.Errorf("AskStopDelegationRequest: %w", err)
	}

	// unlock consumed delegation. A third byte points the delegate lock at the
	// consumed request output, whose ensureStopDelegation carries the allowance
	// that permits the balance decrease. Omitted when nothing is taken, which
	// keeps the ordinary 2-byte form unchanged.
	additional := []byte{ledger.DelegationUnlockedByTarget}
	if r.takeFromDelegation > 0 {
		additional = append(additional, tagAlongOutputIdx)
	}
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), additional...)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(revocationOutputIndex))

	if r.ensureStopDelegation != nil {
		// unlock ensure revocation constraint
		// ensureStopDelegation lives at the next slot after par.Bytes(): [0] amounts,
		// [1] index-values, [2] tagAlongLock, [3] request data, [4] ensureStopDelegation.
		txb.PutUnlockParams(tagAlongOutputIdx, 4, []byte{revocationOutputIndex})
	}

	// the request output's balance, the harvested inflation, and whatever the
	// allowance permitted taking out of the delegation all land on the chain
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(r.Output.TokenBalance() + inflation + r.takeFromDelegation)
	// add negative deltas to the sequencer totals. Vector size is this
	// chain's chainMaxFrozenEpochs (Phase 4 of delegation_epoch_params).
	maxFrozenEpochs := txb.chainMaxFrozenEpochs
	a := oProduce.Amounts()
	for i := byte(0); i < maxFrozenEpochs; i++ {
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage+i] += a.FrozenCoverageAt(i)
	}
	return true, nil
}

func (r *AskStopDelegationRequest) Lines(prefix ...string) *lines.Lines {
	return lines.New(prefix...).Add("AskStopDelegationRequest: delegation ID = " + r.delegationID.StringShort())
}

func (r *AskStopDelegationRequest) AttachmentCostDelta() int {
	// +1 for the consumed tag-along input, +1 for the delegation input, +1 for the unfrozen delegation output
	return 3
}

// NewAskStopDelegationReqOutput builds the askstop command output. allowance
// is how much the target sequencer may take out of the delegation balance as
// compensation; 0 means the fee has to cover all of it.
func NewAskStopDelegationReqOutput(seqID base.ChainID, sender ledger.SigLock, delegationID base.ChainID, fee, allowance uint64) *ledger.Output {
	par := smallkv.New()
	par.Set(FieldCmdCode, []byte{RequestCodeAskStopDelegation})
	par.Set(FieldRevokeDelegationID, delegationID[:])

	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(fee)
		o.WithLock(&ledger.TagAlongLock{
			TargetSequencerID: seqID,
			SenderID:          base.HolderID(sender),
		})
		o.MustPushConstraint(easyfl.InlineDataBytecode(par.Bytes()))
		o.MustPushConstraint((&ledger.EnsureStopDelegation{ChainID: delegationID, Allowance: allowance}).Bytes())
	})
}
