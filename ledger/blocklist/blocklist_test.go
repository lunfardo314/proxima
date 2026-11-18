package blocklist

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestCheck covers the two checks: a frozen output is refused (at every slot;
// the slot gate for signers is applied by the caller), a frozen signer is
// refused, and anything else passes.
func TestCheck(t *testing.T) {
	var frozenSigner base.HolderID
	for h := range frozenHolders {
		frozenSigner = h
		break
	}
	var other base.HolderID
	other[0] = 0x01

	// the input gate tells policy refusals from invalid transactions by ErrFrozen
	require.ErrorIs(t, CheckSigner(frozenSigner), ErrFrozen)
	require.NoError(t, CheckSigner(other))

	var notFrozen base.OutputID
	require.NoError(t, CheckInputs(0, []base.OutputID{notFrozen}))
	for oid := range frozenOutputs {
		if _, unfrozen := unfrozenFromSlot[oid]; unfrozen {
			continue
		}
		require.ErrorIs(t, CheckInputs(0, []base.OutputID{notFrozen, oid}), ErrFrozen)
		require.ErrorIs(t, CheckInputs(^uint32(0), []base.OutputID{notFrozen, oid}), ErrFrozen)
	}
}

// TestUnfrozenFromSlot checks an output taken off the freeze: still on the frozen list, refused
// for transactions before its slot and accepted from its slot on, while every other frozen
// output stays refused at that slot.
func TestUnfrozenFromSlot(t *testing.T) {
	require.NotEmpty(t, unfrozenFromSlot)
	for oid, from := range unfrozenFromSlot {
		_, listed := frozenOutputs[oid]
		require.True(t, listed, "an unfrozen output stays on the frozen list")
		require.ErrorIs(t, CheckInputs(from-1, []base.OutputID{oid}), ErrFrozen)
		require.NoError(t, CheckInputs(from, []base.OutputID{oid}))
		require.NoError(t, CheckInputs(from+1000, []base.OutputID{oid}))
		for other := range frozenOutputs {
			if _, unfrozen := unfrozenFromSlot[other]; !unfrozen {
				require.ErrorIs(t, CheckInputs(from, []base.OutputID{oid, other}), ErrFrozen)
				break
			}
		}
	}
}
