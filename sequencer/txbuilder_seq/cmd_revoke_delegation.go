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

type RevokeDelegationRequest struct {
	o                ledger.OutputWithID
	delegationID     base.ChainID
	delegation       ledger.DelegationOutput // filled up by CheckPreconditions
	ensureRevocation *ledger.EnsureRevocation
}

const (
	RevokeDelegationCmdCode = byte(3)
	FieldRevokeDelegationID = byte(1)
)

func init() {
	registerSequencerCommand(RevokeDelegationCmdCode, _parseRevokeDelegationOutput)
}

func _parseRevokeDelegationOutput(txb *SeqTxBuilder, o ledger.OutputWithID, msg *SeqRequestMessage) (cmd TxBuilderCommand, valid bool, reason error) {
	if o.Output.NumConstraints() > 4 {
		// unexpected structure -> may be attack
		reason = fmt.Errorf("RevokeDelegationRequest: failed to parse")
		return
	}
	// ---------- fetch delegation output from the baseline state
	delegationID, reason := base.ChainIDFromBytes(msg.Get(FieldRevokeDelegationID))
	if reason != nil {
		reason = fmt.Errorf("RevokeDelegationRequest: parsing delegationID: %w", reason)
		return
	}
	ret := &RevokeDelegationRequest{
		o:            o,
		delegationID: delegationID,
	}
	rdr := multistate.MakeSugared(txb.rdr)
	_dOut, reason := rdr.GetChainOutputWithChainID(delegationID)
	if reason != nil {
		// wrong chain ID
		reason = fmt.Errorf("RevokeDelegationRequest: failed to find delegation output %s: %w", delegationID.StringShort(), reason)
		return
	}

	var ok bool
	ret.delegation, ok = ledger.DelegationOutputFromOutputWithChainID(&_dOut)
	if !ok {
		// is not a valid delegation chain output
		reason = fmt.Errorf("RevokeDelegationRequest: failed to parse delegation output %s: %w", delegationID.StringShort(), reason)
		return
	}
	// ----------

	// ---------- check if revocation even makes sense
	if !ret.delegation.IsUnlockableByTarget(uint32(o.Timestamp().Slot)) {
		// cannot be unlocked by target in the slot
		valid = true
		reason = fmt.Errorf("RevokeDelegationRequest: delegation %s is not unlockable by the target in %s",
			delegationID.String(), txb.TransactionData.Timestamp.String())
		return
	}
	// ---------- authenticate: check if the sender of the request and the sequencer must be entitled to revoke particular delegation ID
	if ret.delegation.Target.ChainID() != txb.chainInput.ChainID {
		// this sequencer cannot revoke specific delegation
		reason = fmt.Errorf("RevokeDelegationRequest: the sequencer cannot revoke delegation %s (fail auth)", delegationID.String())
		return
	}
	master, ok := ret.delegation.MasterLock.(ledger.AddressED25519)
	if !ok {
		// wrong master (cannot be)
		reason = fmt.Errorf("RevokeDelegationRequest: inconsistecy while checking master lock")
		return
	}
	if !bytes.Equal(msg.SenderHash[:], master) {
		// this sender cannot revoke delegation -> may be an attack
		reason = fmt.Errorf("RevokeDelegationRequest: sender with hash %s cannot revoke delegation %s (fail auth)",
			hex.EncodeToString(msg.SenderHash[:]), delegationID.String())
		return
	}
	//------------

	// ------------ check if revocation makes economic sense for the sequencer:
	// tokens provided in the tag-along output must at least cover the remaining projected inflation from the frozen amount
	unfreezeSlot := ret.delegation.UnfreezeSlot()
	util.Assertf(unfreezeSlot > txb.TransactionData.Timestamp.Slot.Uint32(), "unfreezeSlot > txb.TransactionData.Timestamp.Slot.Uint32()")

	const patienceMargin = 6
	lostSlots := txb.TransactionData.Timestamp.Slot.Uint32() - unfreezeSlot
	if lostSlots <= patienceMargin {
		// less than 1 min slots until the end of the freeze, refuse to revoke.
		// Just 1 min of patience, and it will be released to the safe revocation window without revocation command
		reason = fmt.Errorf("RevokeDelegationRequest: less than %d slots remain until safe revocation window. Wait a bit", patienceMargin)
		return
	}
	// all token balance on the delegation output is frozen and available for the sequencer to generate inflation
	neededCompensation := ledger.L().ChainInflation(ret.delegation.Output.TokenBalance(), uint32(txb.TransactionData.Timestamp.Slot), lostSlots)
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

func NewRevokeDelegationCommandConstraint(privKey ed25519.PrivateKey, delegationID base.ChainID) ledger.Constraint {
	body := base.NewSmallPersistentMap()
	body.Set(FieldCmdCode, []byte{RevokeDelegationCmdCode})
	body.Set(FieldRevokeDelegationID, delegationID[:])

	msg := ledger.NewMessageWithED25519SenderFromPrivateKey(privKey, body.Bytes())
	return msg
}

func NewRevokeDelegationCommandOutput(targetChain base.ChainID, privKey ed25519.PrivateKey, fee uint64, delegationID base.ChainID) *ledger.Output {
	ensureDelegation := ledger.EnsureRevocation{ChainID: delegationID}
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(fee).WithLock(ledger.ChainLockFromChainID(targetChain))
		o.MustPushConstraint(NewRevokeDelegationCommandConstraint(privKey, delegationID).Bytes())
		o.MustPushConstraint(ensureDelegation.Bytes())
	})
}

func (r *RevokeDelegationRequest) Apply(txb *SeqTxBuilder) (bool, error) {
	// need to reserve at least 2 outputs
	if len(txb.ConsumedOutputs) > 254 {
		return true, fmt.Errorf("RevokeDelegationRequest: too many outputs to consume")
	}
	if len(txb.TransactionData.Outputs) > 255 {
		return true, fmt.Errorf("RevokeDelegationRequest: too many outputs to produce")
	}
	inflation := ledger.L().ChainInflationOneSlot(r.delegation.Output.TokenBalance(), uint32(r.delegation.ID.Slot()))

	oProduce, err := r.delegation.MakeDelegationRevokeOutput(ledger.MakeDelegationRevokeOutputParams{
		TxTs:             txb.TransactionData.Timestamp,
		PredOutputIndex:  byte(len(txb.ConsumedOutputs) + 1),
		Inflation:        inflation,
		HarvestInflation: inflation, // take last inflation bit from delegation
	})
	if err != nil {
		return true, fmt.Errorf("RevokeDelegationRequest: %w", err)
	}

	// consume tag-along with the revoke command message
	tagAlongOutputIdx, err := txb.ConsumeTagAlongOutputUnlock(r.o.Output, r.o.ID, 0, txb.chainInput.ChainConstraintIndex)
	util.AssertNoError(err)
	// consume the delegation predecessor
	predIdx, err := txb.ConsumeOutput(r.delegation.Output, r.delegation.ID)
	util.AssertNoError(err)

	// produce revoked delegation output
	revocationOutputIndex, err := txb.ProduceOutput(oProduce)

	// unlock consumed delegation
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0, 2), ledger.DelegationUnlockedByTarget)
	txb.PutUnlockParams(predIdx, 2, ledger.NewChainUnlockParams(revocationOutputIndex, 2))

	if r.ensureRevocation != nil {
		// unlock ensure revocation constraint
		txb.PutUnlockParams(tagAlongOutputIdx, 3, []byte{revocationOutputIndex})
	}

	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(r.o.Output.TokenBalance() + inflation)
	maxFrozenEpochs := byte(ledger.DelegationConst().MaxFrozenEpochs)
	a := oProduce.Amounts()
	// add negative deltas to the sequencer totals
	for i := byte(0); i < maxFrozenEpochs; i++ {
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage+i] += a.FrozenCoverageAt(i)
	}
	return true, nil
}
