package multistate

// AccountsByLocks totals the UTXO set per lock, Holdings per holder. The
// genesis state is a known fixture: the controller address holds the sequencer
// origin and the controller dust, the stem and the mine chain are one output
// each, and the balances add up to the initial supply.

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestAccountsByLocks_Genesis(t *testing.T) {
	ledger.InitWithTestingLedgerData()

	store := common.NewInMemoryKVStore()
	_, root := InitStateStoreFromGlobals(store)
	rdr := MakeSugared(MustNewReadable(store, root))

	byLock := rdr.AccountsByLocks()
	require.Len(t, byLock, 3, "controller sigLock, stem, mine chain")

	lib := ledger.L(0)
	controller := ledger.SigLockFromED25519PublicKey(lib.GenesisControllerPublicKey()).String()
	require.Equal(t, 2, byLock[controller].NumOutputs)
	require.EqualValues(t, lib.InitialSupply-ledger.GenesisMineChainDust, byLock[controller].Balance)

	stem := ledger.GenesisStemOutput().Output.Lock().String()
	require.Equal(t, 1, byLock[stem].NumOutputs)

	var numOutputs int
	var total uint64
	for _, ai := range byLock {
		numOutputs += ai.NumOutputs
		total += ai.Balance
	}
	require.Equal(t, 4, numOutputs)
	require.EqualValues(t, lib.InitialSupply, total)
}

// The controller is the only holder: its total includes the sequencer origin,
// its idle capital is the dust alone. The stem and the mine chain have no
// single holder and land in Other.
func TestHoldings_Genesis(t *testing.T) {
	ledger.InitWithTestingLedgerData()

	store := common.NewInMemoryKVStore()
	_, root := InitStateStoreFromGlobals(store)
	rdr := MakeSugared(MustNewReadable(store, root))

	h := rdr.Holdings(100)
	require.False(t, h.Truncated)
	require.Equal(t, 4, h.NumScanned)
	require.Len(t, h.Holders, 1)

	lib := ledger.L(0)
	controller := base.HolderID(ledger.SigLockFromED25519PublicKey(lib.GenesisControllerPublicKey()))
	require.Equal(t, 2, h.Holders[controller].NumOutputs)
	require.EqualValues(t, lib.InitialSupply-ledger.GenesisMineChainDust, h.Holders[controller].Total)
	require.EqualValues(t, 1, h.Holders[controller].Idle)

	require.Equal(t, 2, h.Other.NumOutputs)
	require.EqualValues(t, lib.InitialSupply, h.Holders[controller].Total+h.Other.Total)
}

// The scan stops at the cap and says so, instead of walking the whole state.
func TestHoldings_Truncated(t *testing.T) {
	ledger.InitWithTestingLedgerData()

	store := common.NewInMemoryKVStore()
	_, root := InitStateStoreFromGlobals(store)
	rdr := MakeSugared(MustNewReadable(store, root))

	h := rdr.Holdings(3)
	require.True(t, h.Truncated)
	require.Equal(t, 3, h.NumScanned)

	// a cap equal to the state size is not a truncation
	h = rdr.Holdings(4)
	require.False(t, h.Truncated)
	require.Equal(t, 4, h.NumScanned)
}
