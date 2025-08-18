package txbuilder_seq

import "github.com/lunfardo314/proxima/ledger/base"

type RevokeDelegationCommand struct {
	SequencerCommandBase
	delegationID base.ChainID
}

const (
	RevokeDelegationCmdCode = byte(3)
	FieldRevokeDelegationID = byte(1)
)
