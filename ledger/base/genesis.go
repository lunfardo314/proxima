package base

import (
	"encoding/hex"

	"github.com/lunfardo314/easyfl/easyfl_util"
)

const (
	GenesisOutputIndex               = byte(0)
	GenesisStemOutputIndex           = byte(1)
	GenesisControllerDustOutputIndex = byte(2)

	// BoostrapSequencerIDHex is constant on all ledgers
	// This is the first ChainIDLength (24) bytes of the blake2b hash of the
	// genesis output ID (tx ID + output index 0)
	BoostrapSequencerIDHex = "9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a82"
)

// BoostrapSequencerID is a constant
var BoostrapSequencerID ChainID

// init BoostrapSequencerID constant and check consistency

func init() {
	data, err := hex.DecodeString(BoostrapSequencerIDHex)
	easyfl_util.AssertNoError(err)
	BoostrapSequencerID, err = ChainIDFromBytes(data)
	easyfl_util.AssertNoError(err)
	// calculate directly and check
	oid := GenesisOutputID()
	bootSeqIDDirect := MakeOriginChainID(oid)
	easyfl_util.Assertf(BoostrapSequencerID == bootSeqIDDirect, "BoostrapSequencerID must equal MakeOriginChainID(genesisOutputID), got %s", bootSeqIDDirect.StringHex())
}

// GenesisTransactionIDShort set max index of produced UTXOs to 2
// (genesis output at 0, stem output at 1, controller mote output at 2)
func GenesisTransactionIDShort() (ret TransactionIDShort) {
	ret[0] = 2
	return
}

// GenesisTransactionID independent on any ledger constants
func GenesisTransactionID() TransactionID {
	return NewTransactionID(LedgerTime{}, GenesisTransactionIDShort(), true)
}

// GenesisOutputID independent on ledger constants, except GenesisOutputIndex which is byte(0)
func GenesisOutputID() (ret OutputID) {
	// we are placing sequencer flag = true into the genesis tx id to please sequencer constraint
	// of the origin branch transaction. It is the only exception
	ret = MustNewOutputID(GenesisTransactionID(), GenesisOutputIndex)
	return
}

// GenesisStemOutputID independent on ledger constants, except GenesisStemOutputIndex which is byte(1)
func GenesisStemOutputID() (ret OutputID) {
	ret = MustNewOutputID(GenesisTransactionID(), GenesisStemOutputIndex)
	return
}

// GenesisControllerDustOutputID returns the output ID for the controller's mote output (index 2)
// This ensures the controller always has at least one output to create transactions
func GenesisControllerDustOutputID() (ret OutputID) {
	ret = MustNewOutputID(GenesisTransactionID(), GenesisControllerDustOutputIndex)
	return
}
