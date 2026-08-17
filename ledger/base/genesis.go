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

	// The genesis chain outputs are inserted directly into the state and never
	// validated as produced, so their chain IDs can be chosen freely (they only
	// need to be carried explicitly by the genesis chain constraints). We pick
	// fixed, human-readable 24-byte ASCII values.

	// BoostrapSequencerIDName is the 24-byte ASCII source of the bootstrap
	// sequencer chain ID.
	BoostrapSequencerIDName = "Proxima.bootstrap.chain."
	// MineChainIDName is the 24-byte ASCII source of the fair-launch mine chain
	// ID. See claude/launch_rationale.md.
	MineChainIDName = "Proxima.fairlaunch.mine!"

	// BoostrapSequencerIDHex / MineChainIDHex are the hex forms of the ASCII
	// names above, constant on all ledgers. init() cross-checks the two.
	BoostrapSequencerIDHex = "50726f78696d612e626f6f7473747261702e636861696e2e"
	MineChainIDHex         = "50726f78696d612e666169726c61756e63682e6d696e6521"
)

// BoostrapSequencerID and MineChainID are fixed constants, independent of the
// genesis output IDs. The genesis chain outputs carry them explicitly.
var (
	BoostrapSequencerID ChainID
	MineChainID         ChainID
)

// init the constant chain IDs from the hardcoded hex and cross-check them
// against the readable ASCII names.

func init() {
	data, err := hex.DecodeString(BoostrapSequencerIDHex)
	easyfl_util.AssertNoError(err)
	BoostrapSequencerID, err = ChainIDFromBytes(data)
	easyfl_util.AssertNoError(err)
	bootFromName, err := ChainIDFromBytes([]byte(BoostrapSequencerIDName))
	easyfl_util.AssertNoError(err)
	easyfl_util.Assertf(BoostrapSequencerID == bootFromName,
		"BoostrapSequencerID must equal []byte(%q)", BoostrapSequencerIDName)

	data, err = hex.DecodeString(MineChainIDHex)
	easyfl_util.AssertNoError(err)
	MineChainID, err = ChainIDFromBytes(data)
	easyfl_util.AssertNoError(err)
	mineFromName, err := ChainIDFromBytes([]byte(MineChainIDName))
	easyfl_util.AssertNoError(err)
	easyfl_util.Assertf(MineChainID == mineFromName,
		"MineChainID must equal []byte(%q)", MineChainIDName)
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
