package multistate

// AccountsByLocks totals the UTXO set per lock. The genesis state is a
// known fixture: the controller address holds the sequencer origin and the
// controller dust, the stem and the mine chain are one output each, and the
// per-lock balances add up to the initial supply. With sequencer outputs
// skipped, as the idle-capital API does, the controller keeps only the dust.

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestAccountsByLocks_Genesis(t *testing.T) {
	ledger.InitWithTestingLedgerData()

	store := common.NewInMemoryKVStore()
	_, root := InitStateStoreFromGlobals(store)
	rdr := MakeSugared(MustNewReadable(store, root))

	byLock := rdr.AccountsByLocks(nil)
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

	idle := rdr.AccountsByLocks(func(o *ledger.Output) bool { return o.IsSequencerOutput() })
	require.Equal(t, 1, idle[controller].NumOutputs)
	require.EqualValues(t, 1, idle[controller].Balance)
}
