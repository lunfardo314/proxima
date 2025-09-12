package txbuilder_seq

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
)

type AskStopDelegationRequest struct {
	o                ledger.OutputWithID
	delegationID     base.ChainID
	delegation       ledger.DelegationOutput
	ensureRevocation *ledger.EnsureStopDelegation
}

const (
	AskStopDelegationCmdCode = byte(3)
	FieldRevokeDelegationID  = byte(1)
)

func init() {
	registerSequencerCommand(AskStopDelegationCmdCode, _parseAskStopDelegationOutput)
}

func _parseAskStopDelegationOutput(txb *SeqTxBuilder, o ledger.OutputWithID, msg *SeqRequestMessage) (cmd TxBuilderCommand, valid bool, reason error) {
	if o.Output.NumConstraints() > 4 {
		// unexpected structure -> may be attack
		reason = fmt.Errorf("AskStopDelegationRequest: wrong output structure")
		return
	}
	// ---------- fetch delegation output from the baseline state
	delegationID, reason := base.ChainIDFromBytes(msg.Get(FieldRevokeDelegationID))
	if reason != nil {
		reason = fmt.Errorf("AskStopDelegationRequest: parse failed: %w", reason)
		return
	}
	ret := &AskStopDelegationRequest{
		o:            o,
		delegationID: delegationID,
	}
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
	master, ok := ret.delegation.Master().(ledger.AddressED25519)
	if !ok {
		// wrong master (cannot be)
		reason = fmt.Errorf("AskStopDelegationRequest: inconsistecy while checking master lock")
		return
	}
	if !bytes.Equal(msg.SenderHash[:], master) {
		// this sender cannot revoke delegation -> may be an attack
		reason = fmt.Errorf("AskStopDelegationRequest: sender with hash %s cannot revoke delegation %s (failed authorisation)",
			hex.EncodeToString(msg.SenderHash[:]), delegationID.String())
		return
	}

	//------------

	// ------------ check if revocation makes economic sense for the sequencer:
	// tokens provided in the tag-along output must at least cover the remaining projected inflation from the frozen amount
	unfreezeSlot := ret.delegation.UnfreezeSlot()
	util.Assertf(unfreezeSlot > txb.Slot(), "unfreezeSlot > txb.Slot()")

	const patienceMargin = 6
	lostSlots := txb.Slot() - unfreezeSlot
	if lostSlots <= patienceMargin {
		// less than 1 min slots until the end of the freeze, refuse to revoke.
		// Just 1 min of patience, and it will be released to the safe revocation window without revocation command
		reason = fmt.Errorf("AskStopDelegationRequest: less than %d slots remain until safe revocation window. Wait a bit", patienceMargin)
		return
	}
	// all token balance on the delegation output is frozen and available for the sequencer to generate inflation
	neededCompensation := ledger.ChainInflation(ret.delegation.Output.TokenBalance(), txb.Slot(), lostSlots)
	if neededCompensation < o.Output.TokenBalance() {
		// projected inflation advance is bigger than number of tokens in the revocation output
		// -> sequencer do not want loss -> ignore the revocation request
		return
	}
	// check if 'ensure revocation' constraint exists, if yes, sequencer will need to unlock it
	if ens, idx := o.Output.EnsureRevocationConstraint(); idx != 0xff {
		if idx != 3 || ens.ChainID != delegationID {
			// wrong structure. Ensure revocation constraint expected at index 3
			return
		}
		ret.ensureRevocation = ens
	}
	return ret, true, nil
}

func NewAskStopDelegationReqConstraint(privKey ed25519.PrivateKey, delegationID base.ChainID) ledger.Constraint {
	body := base.NewSmallPersistentMap()
	body.Set(FieldCmdCode, []byte{AskStopDelegationCmdCode})
	body.Set(FieldRevokeDelegationID, delegationID[:])

	msg := ledger.NewMessageWithED25519SenderFromPrivateKey(privKey, body.Bytes())
	return msg
}

func (r *AskStopDelegationRequest) Apply(txb *SeqTxBuilder) (valid bool, err error) {
	// need to reserve at least 2 outputs
	if len(txb.ConsumedOutputs) > 254 {
		return true, fmt.Errorf("AskStopDelegationRequest: too many outputs to consume")
	}
	if len(txb.TransactionData.Outputs) > 255 {
		return true, fmt.Errorf("AskStopDelegationRequest: too many outputs to produce")
	}
	inflation := ledger.ChainInflationOneSlot(r.delegation.Output.TokenBalance(), uint32(r.delegation.ID.Slot()))

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
	tagAlongOutputIdx, err := txb.ConsumeTagAlongOutputUnlock(r.o.Output, r.o.ID, 0, txb.chainInput.ChainConstraintIndex)
	util.AssertNoError(err)
	// consume the delegation predecessor
	predIdx, err := txb.ConsumeOutput(r.delegation.Output, r.delegation.ID)
	util.AssertNoError(err)

	// produce revokeRequests delegation output
	revocationOutputIndex, err := txb.ProduceOutput(oProduce)

	// unlock consumed delegation
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0, 2), ledger.DelegationUnlockedByTarget)
	txb.PutUnlockParams(predIdx, 2, ledger.NewChainUnlockParams(revocationOutputIndex, 2))

	if r.ensureRevocation != nil {
		// unlock ensure revocation constraint
		txb.PutUnlockParams(tagAlongOutputIdx, 3, []byte{revocationOutputIndex})
	}

	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(r.o.Output.TokenBalance() + inflation)
	maxFrozenEpochs := byte(ledger.Const.MaxFrozenEpochs)
	a := oProduce.Amounts()
	// add negative deltas to the sequencer totals
	for i := byte(0); i < maxFrozenEpochs; i++ {
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage+i] += a.FrozenCoverageAt(i)
	}
	return true, nil
}

func (r *AskStopDelegationRequest) String() string {
	return "AskStopDelegationRequest: id = " + r.delegationID.StringShort()
}
