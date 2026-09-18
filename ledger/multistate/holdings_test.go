package multistate

// AccountsByLocks totals the UTXO set per lock, Holdings per holder. The
// genesis state is a known fixture: the controller address holds the sequencer
// origin and the controller dust, the stem and the mine chain are one output
// each, and the balances add up to the initial supply.

import (
	"crypto/ed25519"
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

// A delegation belongs to its master. Frozen in the slot of the state it is
// working capital; not frozen it earns nothing and counts as idle. The two
// delegations of a fresh master are written straight into a state on top of
// genesis, one in each state, so the scan is exercised without a sequencer
// transaction.
func TestHoldings_Delegations(t *testing.T) {
	ledger.InitWithTestingLedgerData()

	store := common.NewInMemoryKVStore()
	seqID, root := InitStateStoreFromGlobals(store)

	const notFrozenAmount, frozenAmount = 1_000_000_000, 2_000_000_000
	masterPub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	master := base.HolderID(ledger.SigLockFromED25519PublicKey(masterPub))
	stemID := base.GenesisStemOutputID()
	ts := stemID.Timestamp()
	par := ledger.MakeDelegateInitOutputParams{
		Amount:               notFrozenAmount,
		MasterID:             master,
		Target:               seqID,
		RequiredInflationCut: 100,
		StartSlot:            ts.Slot,
	}
	notFrozen := ledger.MakeDelegationInitOutput(par)
	// the same output marked frozen until an epoch far beyond the state's slot
	frozen := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(frozenAmount)
		o.WithLock(ledger.NewDelegateLock(par.Target, par.MasterID, par.RequiredInflationCut))
		o.PutConstraint(ledger.NewChainOrigin(par.StartSlot).Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{LastFrozenEpoch: 1000, State: ledger.DelegateLockStateFrozen}.Bytes())
	})

	muts := NewMutations()
	muts.InsertAddOutputMutation(base.MustNewOutputID(base.RandomTransactionID(false, 0, ts), 0), notFrozen)
	muts.InsertAddOutputMutation(base.MustNewOutputID(base.RandomTransactionID(false, 0, ts), 0), frozen)
	upd := MustNewUpdatable(store, root)
	upd.MustUpdate(muts, &RootRecordParams{
		StemOutputID:  stemID,
		SeqID:         seqID,
		SlotInflation: notFrozenAmount + frozenAmount,
	})
	rdr := MakeSugared(MustNewReadable(store, upd.Root()))

	h := rdr.Holdings(100)
	require.False(t, h.Truncated)
	require.Equal(t, 6, h.NumScanned)

	require.Len(t, h.Holders, 2, "the genesis controller and the master")
	hi := h.Holders[master]
	require.Equal(t, 2, hi.NumOutputs)
	require.EqualValues(t, notFrozenAmount+frozenAmount, hi.Total)
	require.EqualValues(t, notFrozenAmount, hi.Idle, "only the delegation that is not frozen")
}
