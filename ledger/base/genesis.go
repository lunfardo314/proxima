package base

import (
	"encoding/hex"

	"github.com/lunfardo314/easyfl/easyfl_util"
)

const (
	GenesisOutputIndex               = byte(0)
	GenesisStemOutputIndex           = byte(1)
	GenesisControllerDustOutputIndex = byte(2)
	GenesisMineChainOutputIndex      = byte(3)

	// BoostrapSequencerIDHex is constant on all ledgers.
	// This is the first ChainIDLength (24) bytes of the blake2b hash of the
	// genesis output ID (tx ID + output index 0).
	BoostrapSequencerIDHex = "adffaebe21679c48ea416ac1bff6bce817c84648cd5c7d59"
	// MineChainIDHex is the constant chain ID of the fair-launch mine chain:
	// the first 24 bytes of blake2b of the genesis mine-chain output ID
	// (tx ID + output index 3). See claude/fairlaunch.md.
	MineChainIDHex = "5560bf95dca272c6865365e80b35ffeb56d02adc82989c15"
)

// BoostrapSequencerID and MineChainID are constants derived from the genesis
// transaction ID (which is fixed and independent of ledger constants).
var (
	BoostrapSequencerID ChainID
	MineChainID         ChainID
)

// init the constant chain IDs and check consistency with the hardcoded hex

func init() {
	data, err := hex.DecodeString(BoostrapSequencerIDHex)
	easyfl_util.AssertNoError(err)
	BoostrapSequencerID, err = ChainIDFromBytes(data)
	easyfl_util.AssertNoError(err)
	bootSeqIDDirect := MakeOriginChainID(GenesisOutputID())
	easyfl_util.Assertf(BoostrapSequencerID == bootSeqIDDirect, "BoostrapSequencerID must equal MakeOriginChainID(genesisOutputID), got %s", bootSeqIDDirect.StringHex())

	data, err = hex.DecodeString(MineChainIDHex)
	easyfl_util.AssertNoError(err)
	MineChainID, err = ChainIDFromBytes(data)
	easyfl_util.AssertNoError(err)
	mineChainIDDirect := MakeOriginChainID(GenesisMineChainOutputID())
	easyfl_util.Assertf(MineChainID == mineChainIDDirect, "MineChainID must equal MakeOriginChainID(genesisMineChainOutputID), got %s", mineChainIDDirect.StringHex())
}

// GenesisTransactionIDShort sets max index of produced UTXOs to 3
// (genesis output at 0, stem at 1, controller mote at 2, mine chain at 3)
func GenesisTransactionIDShort() (ret TransactionIDShort) {
	ret[0] = 3
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

// GenesisMineChainOutputID returns the output ID for the fair-launch mine chain
// output (index 3). Its MakeOriginChainID is the constant MineChainID.
func GenesisMineChainOutputID() (ret OutputID) {
	ret = MustNewOutputID(GenesisTransactionID(), GenesisMineChainOutputIndex)
	return
}
