package base

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

const (
	GenesisOutputIndex     = byte(0)
	GenesisStemOutputIndex = byte(1)

	// BoostrapSequencerIDHex is constant on all ledgers
	BoostrapSequencerIDHex = "8739faa34a6902e49bc16455bbd642fd3c649e8959d97089e43f214ca57ea0e5"
)

// BoostrapSequencerID is a constant
var BoostrapSequencerID ChainID

// init BoostrapSequencerID constant and check consistency

func init() {
	data, err := hex.DecodeString(BoostrapSequencerIDHex)
	util.AssertNoError(err)
	BoostrapSequencerID, err = ChainIDFromBytes(data)
	util.AssertNoError(err)
	// calculate directly and check
	var zero33 [33]byte
	zero33[0] = 0b10000000
	genesisOutputID := GenesisOutputID()
	bootSeqIDDirect := blake2b.Sum256(genesisOutputID[:])
	util.Assertf(BoostrapSequencerID == bootSeqIDDirect, "BoostrapSequencerID must be equal to the blake2b hash of genesis output id, got %s", hex.EncodeToString(bootSeqIDDirect[:]))
	// more checks
	oid := GenesisOutputID()
	util.Assertf(MakeOriginChainID(oid) == BoostrapSequencerID, "MakeOriginChainID(&oid) == BoostrapSequencerID")
}

// GenesisTransactionIDShort set max index of produced UTXOs to 1
func GenesisTransactionIDShort() (ret TransactionIDShort) {
	ret[0] = 1
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
