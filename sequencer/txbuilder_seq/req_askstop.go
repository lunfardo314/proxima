package txbuilder_seq

import (
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type AskStopDelegationRequest struct {
	ledger.TagAlongOutput
	delegationID         base.ChainID
	delegation           ledger.DelegationOutput
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
	if ret.delegation.Target.ChainID() != txb.chainInput.ChainID {
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
	// all token balance on the delegation output is frozen and available for the sequencer to generate inflation
	neededCompensation := ledger.ChainInflation(ret.delegation.Output.TokenBalance(), txb.Slot(), lostSlots)
	if neededCompensation > o.Output.TokenBalance() {
		// projected inflation advance is bigger than number of tokens in the revocation output
		// -> sequencer do not want loss -> ignore the revocation request
		// fix: bare return left cmd=nil, reason=nil -> nil pointer dereference in AddTagAlongInput
		reason = fmt.Errorf("AskStopDelegationRequest: compensation not sufficient (needed %d, provided %d)", neededCompensation, o.Output.TokenBalance())
		return
	}
	// check if 'ensureStopDelegation' constraint exists, if yes, sequencer will need to unlock it
	if ens, idx := o.Output.EnsureStopDelegationConstraint(); idx != 0xff {
		if idx != 3 || ens.ChainID != delegationID {
			// wrong structure. Ensure revocation constraint expected at index 3
			// fix: bare return left cmd=nil, reason=nil -> nil pointer dereference in AddTagAlongInput
			reason = fmt.Errorf("AskStopDelegationRequest: wrong ensureStopDelegation constraint (idx=%d)", idx)
			return
		}
		ret.ensureStopDelegation = ens
	}
	return ret, true, nil
}

func (r *AskStopDelegationRequest) Apply(txb *SeqTxBuilder) (valid bool, err error) {
	// need to reserve at least 2 outputs
	if len(txb.ConsumedOutputs) > 254 {
		return true, fmt.Errorf("AskStopDelegationRequest: too many outputs to consume")
	}
	if len(txb.TransactionData.Outputs) > 255 {
		return true, fmt.Errorf("AskStopDelegationRequest: too many outputs to produce")
	}
	inflation := ledger.ChainInflationOneSlot(r.delegation.Output.TokenBalance(), r.delegation.ID.Slot())

	oProduce, err := r.delegation.MakeDelegationRevokeOutput(ledger.MakeDelegationRevokeOutputParams{
		TxTs:             txb.Timestamp(),
		PredOutputIndex:  byte(len(txb.ConsumedOutputs) + 1),
		Inflation:        inflation,
		HarvestInflation: inflation, // take last inflation bit from delegation
	})
	if err != nil {
		return true, fmt.Errorf("AskStopDelegationRequest: %w", err)
	}

	// consume tag-along with the revoke command message
	tagAlongOutputIdx, err := txb.ConsumeOutput(r.Output, r.ID)
	util.AssertNoError(err)
	txb.PutUnlockParams(tagAlongOutputIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0, 2))

	// consume the delegation predecessor
	predIdx, err := txb.ConsumeOutput(r.delegation.Output, r.delegation.ID)
	util.AssertNoError(err)

	// produce revokeRequests delegation output
	revocationOutputIndex, err := txb.ProduceOutput(oProduce)
	if err != nil {
		return false, fmt.Errorf("AskStopDelegationRequest: %w", err)
	}

	// unlock consumed delegation
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0, 2), ledger.DelegationUnlockedByTarget)
	txb.PutUnlockParams(predIdx, 2, ledger.NewChainUnlockParams(revocationOutputIndex, 2))

	if r.ensureStopDelegation != nil {
		// unlock ensure revocation constraint
		txb.PutUnlockParams(tagAlongOutputIdx, 3, []byte{revocationOutputIndex})
	}

	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(r.Output.TokenBalance() + inflation)
	maxFrozenEpochs := byte(txb.MaxFrozenEpochs)
	a := oProduce.Amounts()
	// add negative deltas to the sequencer totals
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

func NewAskStopDelegationReqOutput(seqID base.ChainID, sender ledger.SigLock, delegationID base.ChainID, fee uint64) *ledger.Output {
	par := base.NewSmallPersistentMap()
	par.Set(FieldCmdCode, []byte{RequestCodeAskStopDelegation})
	par.Set(FieldRevokeDelegationID, delegationID[:])

	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(fee)
		o.WithLock(&ledger.TagAlongLock{
			TargetSequencerID: seqID,
			SenderID:          base.SpenderID(sender),
		})
		o.MustPushConstraint(easyfl.InlineDataBytecode(par.Bytes()))
		o.MustPushConstraint((&ledger.EnsureStopDelegation{ChainID: delegationID}).Bytes())
	})
}
