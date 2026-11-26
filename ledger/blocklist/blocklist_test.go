package blocklist

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The tables are empty (the outputs and keys of the 2026-11-17 exploit moved to
// ledger/freezeburn), so the tests plant their own entries to check the mechanism.

// plant adds a frozen output, a frozen signer and an output unfrozen from slot to the tables
// and removes them when the test ends.
func plant(t *testing.T, oid, unfrozenOid base.OutputID, signer base.HolderID, from uint32) {
	t.Helper()
	frozenOutputs[oid] = struct{}{}
	frozenOutputs[unfrozenOid] = struct{}{}
	frozenHolders[signer] = struct{}{}
	unfrozenFromSlot[unfrozenOid] = from
	t.Cleanup(func() {
		delete(frozenOutputs, oid)
		delete(frozenOutputs, unfrozenOid)
		delete(frozenHolders, signer)
		delete(unfrozenFromSlot, unfrozenOid)
	})
}

// TestTablesEmpty checks that this release freezes nothing through the blocklist.
func TestTablesEmpty(t *testing.T) {
	require.Empty(t, frozenOutputs)
	require.Empty(t, frozenHolders)
	require.Empty(t, unfrozenFromSlot)
}

// TestCheck covers the two checks: a frozen output is refused (at every slot;
// the slot gate for signers is applied by the caller), a frozen signer is
// refused, and anything else passes.
func TestCheck(t *testing.T) {
	var frozenOid, unfrozenOid, notFrozen base.OutputID
	frozenOid[0], unfrozenOid[0] = 1, 2
	var frozenSigner, other base.HolderID
	frozenSigner[0], other[0] = 1, 2
	plant(t, frozenOid, unfrozenOid, frozenSigner, 1000)

	// the input gate tells policy refusals from invalid transactions by ErrFrozen
	require.ErrorIs(t, CheckSigner(frozenSigner), ErrFrozen)
	require.NoError(t, CheckSigner(other))

	require.NoError(t, CheckInputs(0, []base.OutputID{notFrozen}))
	require.ErrorIs(t, CheckInputs(0, []base.OutputID{notFrozen, frozenOid}), ErrFrozen)
	require.ErrorIs(t, CheckInputs(^uint32(0), []base.OutputID{notFrozen, frozenOid}), ErrFrozen)
}

// TestUnfrozenFromSlot checks an output taken off the freeze: still on the frozen list, refused
// for transactions before its slot and accepted from its slot on, while every other frozen
// output stays refused at that slot.
func TestUnfrozenFromSlot(t *testing.T) {
	var frozenOid, unfrozenOid base.OutputID
	frozenOid[0], unfrozenOid[0] = 1, 2
	var signer base.HolderID
	const from = 1000
	plant(t, frozenOid, unfrozenOid, signer, from)

	require.ErrorIs(t, CheckInputs(from-1, []base.OutputID{unfrozenOid}), ErrFrozen)
	require.NoError(t, CheckInputs(from, []base.OutputID{unfrozenOid}))
	require.NoError(t, CheckInputs(from+1000, []base.OutputID{unfrozenOid}))
	require.ErrorIs(t, CheckInputs(from, []base.OutputID{unfrozenOid, frozenOid}), ErrFrozen)
}
