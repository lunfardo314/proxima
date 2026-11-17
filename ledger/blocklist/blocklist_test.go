package blocklist

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestCheck covers the outcomes of Check: a frozen output is refused at any
// slot, so a spend back-dated before the activation slot is refused too; a
// frozen signer is refused only from the activation slot on, so history it
// signed stays valid; anything else passes.
func TestCheck(t *testing.T) {
	var frozenSigner base.HolderID
	for h := range frozenHolders {
		frozenSigner = h
		break
	}
	var other base.HolderID
	other[0] = 0x01

	require.NoError(t, Check(ActivationSlot-1, frozenSigner, nil))
	require.Error(t, Check(ActivationSlot, frozenSigner, nil))
	require.NoError(t, Check(ActivationSlot, other, nil))

	for oid := range frozenOutputs {
		require.Error(t, Check(0, other, []base.OutputID{oid}))
		require.Error(t, Check(ActivationSlot-1, other, []base.OutputID{oid}))
		require.Error(t, Check(ActivationSlot, other, []base.OutputID{oid}))
	}
}
