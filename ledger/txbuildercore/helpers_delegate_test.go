package txbuildercore_test

// Byte-identity tests for the Phase-C delegation helpers.

import (
	"math"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestNewDelegateLockBytecode_ByteIdentity exercises the wallet
// helper across the three branches of the maxFrozenEpochs first-arg
// heuristic (zero -> "0x", equal-to-target -> "0x", distinct ->
// uint8 literal). Server side compares against
// ledger.NewDelegateLock(...).Bytes().
func TestNewDelegateLockBytecode_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

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
		requiredInflationCut uint16
		epochSlots             uint32
		targetMaxFrozenEpochs  byte
	}{
		{"zero max", 0, 5000, 600, 32},
		{"max equal target", 32, 5000, 600, 32},
		{"max distinct", 16, 7500, 1800, 32},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			walletBin, err := lib.NewDelegateLockBytecode(c.requiredInflationCut)
			require.NoError(t, err)

			serverBin := ledger.NewDelegateLock(target, master, c.requiredInflationCut).Bytes()
			require.Equal(t, serverBin, walletBin)
		})
	}
}

// TestNewDelegateLockState_ByteIdentity checks that the wallet emits
// the same bytes as ledger.DelegateLockState{...}.Bytes() for a few
// representative state values.
func TestNewDelegateLockState_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

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

// TestNewSequencerConstraintBytecode_ByteIdentity checks the wallet's
// sequencer-constraint bytecode matches ledger.NewSequencerConstraint.
func TestNewSequencerConstraintBytecode_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	cases := []struct {
		epochSlots      uint32
		maxFrozenEpochs byte
		coverageDelta   uint64
	}{
		{600, 20, 0},
		{500, 8, 1_000_000},
		{2000, 32, math.MaxUint64},
	}

	for _, c := range cases {
		walletBin, err := lib.NewSequencerConstraintBytecode(c.coverageDelta)
		require.NoError(t, err)
		serverBin := ledger.NewSequencerConstraint(c.coverageDelta).Bytes()
		require.Equal(t, serverBin, walletBin, "epochSlots=%d maxFrozen=%d coverageDelta=%d", c.epochSlots, c.maxFrozenEpochs, c.coverageDelta)
	}
}

// TestNewDelegationInitOutput_ByteIdentity verifies the wallet
// composer produces a delegation chain-origin output byte-identical
// to ledger.MakeDelegationInitOutput.
func TestNewDelegationInitOutput_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	var target base.ChainID
	for i := range target {
		target[i] = byte(i + 11)
	}
	var master base.HolderID
	for i := range master {
		master[i] = byte(i + 99)
	}

	cases := []struct {
		name                   string
		amount                 uint64
		maxFrozenEpochs        byte
		requiredInflationCut uint16
		startSlot              uint32
		epochSlots             uint32
		targetMaxFrozenEpochs  byte
	}{
		{"zero max", 1_000_000, 0, 900, 1234, 600, 32},
		{"max equal target", 5_000_000, 32, 750, 1, 500, 32},
		{"max distinct", 12_345_678, 16, 850, 9999, 1800, 32},
	}

	for _, c := range cases {
		walletOut, err := lib.NewDelegationInitOutput(txbuildercore.DelegationInitOutputParams{
			Amount:                 c.amount,
			MasterID:               master,
			Target:                 target,
			RequiredInflationCut: c.requiredInflationCut,
			StartSlot:              c.startSlot,
		})
		require.NoError(t, err)

		serverOut := ledger.MakeDelegationInitOutput(ledger.MakeDelegateInitOutputParams{
			Amount:                 c.amount,
			MasterID:               master,
			Target:                 target,
			RequiredInflationCut: c.requiredInflationCut,
			StartSlot:              c.startSlot,
		})

		require.Equal(t, serverOut.Bytes(), walletOut.Bytes(), "case: %s", c.name)
	}
}
