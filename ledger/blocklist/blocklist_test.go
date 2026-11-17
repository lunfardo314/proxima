package blocklist

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestCheck covers the three outcomes of Check: nothing is checked before the
// activation slot, a listed signer or a listed input makes the transaction
// invalid from that slot on, and anything else passes.
func TestCheck(t *testing.T) {
	var frozenSigner base.HolderID
	for h := range frozenHolders {
		frozenSigner = h
		break
	}
	var other base.HolderID
	other[0] = 0x01

	// before activation even a frozen signer passes: committed history stays valid
	require.NoError(t, Check(ActivationSlot-1, frozenSigner, nil))

	require.Error(t, Check(ActivationSlot, frozenSigner, nil))
	require.NoError(t, Check(ActivationSlot, other, nil))

	for oid := range frozenOutputs {
		require.NoError(t, Check(ActivationSlot-1, other, []base.OutputID{oid}))
		require.Error(t, Check(ActivationSlot, other, []base.OutputID{oid}))
	}
}
