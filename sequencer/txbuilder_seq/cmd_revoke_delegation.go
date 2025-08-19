package txbuilder_seq

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

type RevokeDelegationCommand struct {
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

func _parseRevokeDelegationOutput(txb *SeqTxBuilder, o ledger.OutputWithID, msg *SeqCommandMessage) (cmd TxBuilderCommand, isValid bool) {
	if o.Output.NumConstraints() != 4 {
		// unexpected structure -> may be attack
		return
	}
	delegationID, err := base.ChainIDFromBytes(msg.Get(FieldRevokeDelegationID))
	if err != nil {
		return
	}
	ret := &RevokeDelegationCommand{
		o:            o,
		delegationID: delegationID,
	}
	rdr := multistate.MakeSugared(txb.rdr)
	_dOut, err := rdr.GetChainOutputWithChainID(delegationID)
	if err != nil {
		// wrong chain ID
		return
	}

	var ok bool
	ret.delegation, ok = ledger.DelegationOutputFromOutputWithChainID(&_dOut)
	if !ok {
		// is not a valid delegation chain output
		return
	}
	// authenticate
	if ret.delegation.Target.ChainID() != txb.chainInput.ChainID {
		// this sequencer cannot revoke specific delegation
		return
	}
	master, ok := ret.delegation.MasterLock.(ledger.AddressED25519)
	if !ok {
		// wrong master (cannot be)
		return
	}
	if msg.SenderHash != blake2b.Sum256(master) {
		// this sender cannot revoke delegation -> may be an attack
		return
	}

	// check if revocation even makes sense
	if ret.delegation.ID.Slot()+1 >= o.Timestamp().Slot ||
		ret.delegation.Revoked ||
		!ret.delegation.IsFrozen(txb.TransactionData.Timestamp.Slot) {
		// too old request, already revoked or not frozen
		return
	}
	// check if tokens provided in the tag-along output compensate for the already commited inflation
	unfreeze := ret.delegation.UnfreezeSlot()
	util.Assertf(unfreeze > txb.TransactionData.Timestamp.Slot.Uint32(), "ret.delegation.IsFrozen(txb.TransactionData.Timestamp.Slot)")

	const patienceMargin = 6 // slots
	lostSlots := txb.TransactionData.Timestamp.Slot.Uint32() - unfreeze
	if lostSlots < patienceMargin {
		// just 1 min of patience and it will be released to the safe revocation window without revocation command
		return
	}
	neededCompensation := ledger.InflationForSlots(ret.delegation.Output.TokenBalance(), lostSlots)
	if neededCompensation < o.Output.TokenBalance() {
		// remaining inflation advance is bigger than number of tokens in the revocation output
		// -> sequencer do not want loss -> ignore the revocation request
		return
	}
	// check if 'ensure revocation' exists, if yes, sequencer will need to unlock it
	if ens, idx := o.Output.EnsureRevocationConstraint(); idx != 0xff {
		if idx != 3 || ens.ChainID != delegationID {
			// wrong structure. Ensure revocation constraint expected at index 3
			return
		}
		ret.ensureRevocation = ens
	}
	return ret, true
}

func NewRevokeDelegationCommandBytecode(privKey ed25519.PrivateKey, delegationID base.ChainID) []byte {
	body := base.NewSmallPersistentMap()
	body.Set(FieldCmdCode, []byte{RevokeDelegationCmdCode})
	body.Set(FieldRevokeDelegationID, delegationID[:])

	msg := ledger.NewMessageWithED25519SenderFromPrivateKey(privKey, body.Bytes())
	return msg.Bytes()
}

func NewRevokeDelegationCommandOutput(targetChain base.ChainID, privKey ed25519.PrivateKey, fee uint64, delegationID base.ChainID) *ledger.Output {
	ensureDelegation := ledger.EnsureRevocation{ChainID: delegationID}
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(fee).WithLock(ledger.ChainLockFromChainID(targetChain))
		o.MustPushConstraint(NewRevokeDelegationCommandBytecode(privKey, delegationID))
		o.MustPushConstraint(ensureDelegation.Bytes())
	})
}

func (r *RevokeDelegationCommand) Apply(txb *SeqTxBuilder) error {
	// need to reserve at least 2 outputs
	if len(txb.ConsumedOutputs) > 254 {
		return fmt.Errorf("RevokeDelegationCommand: too many outputs to consume")
	}
	if len(txb.TransactionData.Outputs) > 255 {
		return fmt.Errorf("RevokeDelegationCommand: too many outputs to produce")
	}
	tagAlongOutputIdx, err := txb.ConsumeTagAlongOutputUnlock(r.o.Output, r.o.ID, 0, txb.chainInput.ChainConstraintIndex)
	util.AssertNoError(err)
	predOutputIdx, err := txb.ConsumeOutput(r.delegation.Output, r.delegation.ID)
	util.AssertNoError(err)

	oProduce, err := r.delegation.MakeDelegationRevokeOutput(ledger.MakeDelegationRevokeOutputParams{
		Timestamp:        txb.TransactionData.Timestamp,
		PredOutputIndex:  predOutputIdx,
		Inflation:        0, // TODO
		HarvestInflation: 0,
	})
	if err != nil {
		return fmt.Errorf("RevokeDelegationCommand: %w", err)
	}
	revocationOutputIndex, err := txb.ProduceOutput(oProduce)
	if r.ensureRevocation != nil {
		// unlock ensure revocation constraint
		txb.PutUnlockParams(tagAlongOutputIdx, 3, []byte{revocationOutputIndex})
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(r.o.Output.TokenBalance() + oProduce.TokenBalance())
	maxFrozenEpochs := byte(ledger.DelegationConst().MaxFrozenEpochs)
	a := oProduce.Amounts()
	for i := byte(0); i < maxFrozenEpochs; i++ {
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage+i] += a.FrozenCoverageAt(i)
	}
	return nil
}
