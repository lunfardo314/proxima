package vertex

import (
	"context"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util/set256"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestSlotCoverageOldImport(t *testing.T) {
	require.EqualValues(t, 1_000, coverageExample(t, false))
}

func TestSlotCoverageCurrentOwner(t *testing.T) {
	require.EqualValues(t, 1_001_000, coverageExample(t, true))
}

// Accounting fixture: native trie/cone, synthetic transactions, no signature claim.
// Slot 100 spends foreign 1,000,000 and own 1,000; slot 101 spends only own funds,
// unless currentOwner adds a genuine foreign successor in slot 101 as well.
func coverageExample(t *testing.T, currentOwner bool) uint64 {
	t.Helper()
	previous := ledger.L(0).DefinitionsJSON()
	upgraded, err := ledger.BuildSlotLocalConsumptionLibrary(previous)
	require.NoError(t, err)
	ledger.MustInitLibraryCacheFromJSON(upgraded)
	t.Cleanup(func() { ledger.MustInitLibraryCacheFromJSON(previous) })
	wrap := func(slot uint32, tick byte, amount uint64) *WrappedTx {
		o := ledger.NewOutput(func(b *ledger.OutputBuilder) {
			b.WithTokenBalance(amount).WithLock(ledger.SigLockFromED25519PrivateKey(pastConeTestGenesisKey))
		})
		w := WrapTxID(base.RandomTransactionID(true, 0, base.T(slot, tick)))
		w.Unwrap(UnwrapOptions{VirtualTx: func(v *VirtualTransaction) { v.mustAddOutput(0, o) }})
		return w
	}
	baseline := wrap(101, 0, 0)
	foreign, own := wrap(99, 9, 1_000_000), wrap(99, 10, 1_000)
	oldForeign, oldOwn := wrap(100, 20, 1_000_000), wrap(100, 25, 1_000)
	current := wrap(101, 30, 1_000)
	foreign.AddConsumer(0, oldForeign)
	own.AddConsumer(0, oldOwn)
	oldOwn.AddConsumer(0, current)
	target := base.T(101, 40)
	pc := NewPastCone(nil, current, target, "coverage-example")
	id := baseline.ID()
	pc.SetBaseline(&id)
	store := common.NewInMemoryKVStore()
	_, initial := multistate.InitStateStoreFromGlobals(store)
	state := multistate.MustNewUpdatable(store, initial)
	muts := multistate.NewMutations()
	var index set256.Set256
	index.Insert(0)
	for _, w := range []*WrappedTx{baseline, foreign, own} {
		muts.InsertAddOutputMutation(w.OutputID(0), w.MustOutputAt(0))
		muts.InsertAddTxMutation(w.ID(), index)
		pc.SetFlagsUp(w, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState|FlagPastConeVertexInTheState)
	}
	state.MustUpdate(muts, nil)
	for _, w := range []*WrappedTx{oldForeign, oldOwn, current} {
		pc.SetFlagsUp(w, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState)
	}
	if currentOwner {
		next := wrap(101, 31, 1_000_000)
		oldForeign.AddConsumer(0, next)
		pc.SetFlagsUp(next, FlagPastConeVertexKnown|FlagPastConeVertexCheckedInTheState)
	}
	reader := multistate.MustNewSugaredReadableState(store, state.Root()).IndexedStateReader
	read := func(base.TransactionID) multistate.StateReader { return reader }
	got, err := pc.CoverageDeltaRaw(context.Background(), read)
	require.NoError(t, err)
	return got
}
