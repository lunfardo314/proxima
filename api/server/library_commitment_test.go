package server

import (
	"encoding/hex"
	"testing"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// commitmentEnv is the slice of the server environment libraryCommitment
// touches: the latest reliable branch, the state store and the tx store. The
// embedded nil interface makes any other call panic, which is the point.
type commitmentEnv struct {
	environment
	lrb     *multistate.BranchData
	store   global.Store
	txStore global.TxBytesStore
}

func (e *commitmentEnv) GetLatestReliableBranch() *multistate.BranchData { return e.lrb }
func (e *commitmentEnv) StateStore() global.Store                        { return e.store }
func (e *commitmentEnv) TxBytesStore() global.TxBytesStore               { return e.txStore }
func (e *commitmentEnv) GetTxBytes(txid *base.TransactionID) []byte {
	return e.txStore.GetTxBytes(txid)
}

// The node's half of kb/library_proof.md: the commitment built for the latest
// reliable branch verifies wallet-side against the library the node serves,
// and the slot whose upgrade UTXO is not in the baseline is reported as an
// error instead of a proof.
func TestLibraryCommitment(t *testing.T) {
	ledger.InitWithTestingLedgerData()
	store := common.NewInMemoryKVStore()
	_, genesisRoot := multistate.InitStateStoreFromGlobals(store)

	// a branch at slot 1 whose baseline is the genesis state
	stem := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(0).WithLock(&ledger.StemLock{})
		o.PutConstraint((&ledger.OracleData{BaselineRoot: genesisRoot.Bytes()}).Bytes(), ledger.ConstraintIndexChain)
	})
	branchBytes := txbuildercore.SerializeRawTxBytes(&txbuildercore.TxRawData{
		Timestamp:            base.LedgerTime{Slot: 1},
		SequencerOutputIndex: 0,
		SequencerData:        []byte{0, 1},
		OutputBytes:          [][]byte{stem.Bytes(), stem.Bytes()},
	})
	branchID, err := txbuildercore.TxIDFromBytes(branchBytes)
	require.NoError(t, err)
	txStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
	_, err = txStore.PersistTxBytes(branchBytes, branchID)
	require.NoError(t, err)

	srv := &server{environment: &commitmentEnv{
		lrb:     &multistate.BranchData{Stem: &ledger.OutputWithID{ID: base.MustNewOutputID(branchID, 1), Output: stem}},
		store:   store,
		txStore: txStore,
	}}

	lib := ledger.L(0)
	prevHash := lib.UpgradeChainData().PrevLibraryHash
	def := &txbuildercore.LedgerDefinitionJSON{
		UpgradeSlot:     0,
		LibraryJSON:     string(lib.DefinitionsJSON()),
		PrevLibraryHash: hex.EncodeToString(prevHash[:]),
		PrevUpgradeSlot: base.MaxSlot,
		Commitment:      srv.libraryCommitment(0),
	}
	require.Empty(t, def.Commitment.Error)
	walletLib, c, err := txbuildercore.LibraryFromLedgerDefinition(def)
	require.NoError(t, err)
	require.Equal(t, branchID, c.BranchID)
	require.Equal(t, lib.LibraryHash(), walletLib.LibraryHash())

	// an upgrade the baseline does not hold yet
	c7 := srv.libraryCommitment(7)
	require.Contains(t, c7.Error, "not yet in the baseline")

	// no reliable branch at all
	srv = &server{environment: &commitmentEnv{}}
	require.Contains(t, srv.libraryCommitment(0).Error, "not been found")
}
