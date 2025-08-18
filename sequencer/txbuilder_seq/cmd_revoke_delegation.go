package txbuilder_seq

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"golang.org/x/crypto/blake2b"
)

type RevokeDelegationCommand struct {
	o                ledger.OutputWithID
	delegationID     base.ChainID
	delegationUTXO   ledger.DelegateOutput // filled up by CheckPreconditions
	ensureRevocation ledger.EnsureRevocation
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
	ret.delegationUTXO, ok = ledger.DelegateOutputFromOutputWithChainID(&_dOut)
	if !ok {
		// is not a valid delegation chain output
		return
	}
	// authenticate
	master, ok := ret.delegationUTXO.MasterLock.(ledger.AddressED25519)
	if !ok {
		// wrong master (cannot be)
		return
	}
	if msg.SenderHash != blake2b.Sum256(master) {
		// wrong sender -> may be attack
		return
	}
	ens, idx := o.Output.EnsureRevocationConstraint()
	if idx != 3 || ens.ChainID != delegationID {
		// wrong structure. Ensure revocation constraint expected at index 3
		return
	}
	ret.ensureRevocation = *ens
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
	// TODO
	panic("not implemented")
}
