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
	require.NoError(t, CheckInputs([]base.OutputID{notFrozen}))
	for oid := range frozenOutputs {
		require.ErrorIs(t, CheckInputs([]base.OutputID{notFrozen, oid}), ErrFrozen)
	}
}
