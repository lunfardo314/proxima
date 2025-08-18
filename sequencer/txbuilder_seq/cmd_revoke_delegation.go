package txbuilder_seq

import (
	"bytes"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
)

type RevokeDelegationCommand struct {
	SequencerCommandBase
	delegationID   base.ChainID
	delegationUTXO ledger.DelegateOutput // filled up by CheckPreconditions
}

const (
	RevokeDelegationCmdCode = byte(3)
	FieldRevokeDelegationID = byte(1)
)

func init() {
	registerSequencerCommand(RevokeDelegationCmdCode, func(cmdBase SequencerCommandBase) (SequencerCommand, bool) {
		delegationID, err := base.ChainIDFromBytes(cmdBase.Get(FieldRevokeDelegationID))
		if err != nil {
			return nil, false
		}
		if err != nil {
			return nil, false
		}
		return &RevokeDelegationCommand{
			SequencerCommandBase: cmdBase,
			delegationID:         delegationID,
		}, true
	})
}

func (r *RevokeDelegationCommand) CheckPreconditions(txb *SequencerTxBuilder) (isAuth bool, consume bool, producesOutputs int) {
	// retrieves delegation output by chainID from the state. Checks if master lock (owner's public key hash) of
	// the delegation output is equal to the sender hash
	sugared := multistate.MakeSugared(txb.rdr)
	out, err := sugared.GetChainOutputWithID(r.delegationID)
	if err != nil {
		return false, false, 0
	}
	var ok bool
	if r.delegationUTXO, ok = ledger.AsDelegateOutput(out.Output, out.ID); !ok {
		return false, false, 0
	}
	masterAddr, ok := r.delegationUTXO.MasterLock.(ledger.AddressED25519)
	if !ok {
		return false, false, 0
	}
	if isAuth = bytes.Equal(masterAddr, r.SenderHash[:]); isAuth {
		producesOutputs = 1
	}
	return isAuth, isAuth, producesOutputs
}

func (r *RevokeDelegationCommand) Apply(txb *SequencerTxBuilder) {
	// TODO
}

func (r *RevokeDelegationCommand) ProducesAdditionalOutputs() int {
	return 1
}
