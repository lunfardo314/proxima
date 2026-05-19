package txcore_test

// Byte-identity tests for the Phase-C delegation helpers.

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestNewDelegateLockBytecode_ByteIdentity exercises the wallet
// helper across the three branches of the maxFrozenEpochs first-arg
// heuristic (zero -> "0x", equal-to-target -> "0x", distinct ->
// uint8 literal). Server side compares against
// ledger.NewDelegateLock(...).Bytes().
func TestNewDelegateLockBytecode_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	var target base.ChainID
	for i := range target {
		target[i] = byte(i + 1)
	}
	var master base.HolderID
	for i := range master {
		master[i] = byte(i + 100)
	}

	cases := []struct {
		name                   string
		maxFrozenEpochs        byte
		requiredInflationShare uint16
		epochSlots             uint32
		targetMaxFrozenEpochs  byte
	}{
		{"zero max", 0, 5000, 600, 32},
		{"max equal target", 32, 5000, 600, 32},
		{"max distinct", 16, 7500, 1800, 32},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			walletBin, err := lib.NewDelegateLockBytecode(
				c.maxFrozenEpochs, c.requiredInflationShare,
				c.epochSlots, c.targetMaxFrozenEpochs,
			)
			require.NoError(t, err)

			serverBin := ledger.NewDelegateLock(
				target, master,
				c.maxFrozenEpochs, c.requiredInflationShare,
				c.epochSlots, c.targetMaxFrozenEpochs,
			).Bytes()
			require.Equal(t, serverBin, walletBin)
		})
	}
}

// TestNewDelegateLockState_ByteIdentity checks that the wallet emits
// the same bytes as ledger.DelegateLockState{...}.Bytes() for a few
// representative state values.
func TestNewDelegateLockState_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	cases := []struct {
		lastFrozenEpoch uint32
		state           byte
	}{
		{0, 0},   // chain-origin zero state
		{42, 1},  // frozen
		{1024, 2}, // on hold
	}

	for _, c := range cases {
		walletBin, err := lib.NewDelegateLockState(c.lastFrozenEpoch, c.state)
		require.NoError(t, err)
		serverBin := ledger.DelegateLockState{
			LastFrozenEpoch: c.lastFrozenEpoch,
			State:           c.state,
		}.Bytes()
		require.Equal(t, serverBin, walletBin, "lastFrozen=%d state=%d", c.lastFrozenEpoch, c.state)
	}
}

// TestNewDelegationParams_ByteIdentity checks delegationParams
// bytecode matches ledger.NewDelegationParams.
func TestNewDelegationParams_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	cases := []struct {
		epochSlots      uint32
		maxFrozenEpochs byte
	}{
		{600, 20},
		{500, 8},
		{2000, 32},
	}

	for _, c := range cases {
		walletBin, err := lib.NewDelegationParams(c.epochSlots, c.maxFrozenEpochs)
		require.NoError(t, err)
		serverBin := ledger.NewDelegationParams(c.epochSlots, c.maxFrozenEpochs).Bytes()
		require.Equal(t, serverBin, walletBin, "epochSlots=%d maxFrozen=%d", c.epochSlots, c.maxFrozenEpochs)
	}
}
