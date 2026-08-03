package txbuildercore_test

// Byte-identity tests for the wallet-side mine helpers: the mineLock
// bytecode plus its round-trip parse. Wallet-emitted bytes must match
// the ledger.MineLock constructor byte-for-byte so `proxi node mine`
// builds transitions the server accepts.

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
)

// TestNewMineLock_ByteIdentity verifies the wallet mineLock bytecode
// matches ledger.NewMineLock(r, b).Bytes() across the zero-elided and
// fully-populated cases.
func TestNewMineLock_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	cases := []struct {
		r, b uint64
	}{
		{0, 0},                       // all elided
		{900_000_000_000_000, 24},    // typical
		{500_000_000, 40},            // wide R, ceiling difficulty
	}
	for _, c := range cases {
		walletBin, err := lib.NewMineLock(c.r, c.b)
		require.NoError(t, err)
		serverBin := ledger.NewMineLock(c.r, c.b).Bytes()
		require.Equal(t, serverBin, walletBin, "case %+v", c)
	}
}

// TestParseMineLock_RoundTrip verifies the wallet parser decodes the
// ledger-emitted bytecode back to the same R/B fields.
func TestParseMineLock_RoundTrip(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	const (
		r = uint64(900_000_000_000_000)
		b = uint64(24)
	)
	bin := ledger.NewMineLock(r, b).Bytes()
	view, err := lib.ParseMineLock(bin)
	require.NoError(t, err)
	require.EqualValues(t, r, view.R)
	require.EqualValues(t, b, view.B)
}

// TestMineRequiredK pins the pace-relieved difficulty K = max(B - (M - P), E):
// full B at the minimum pace P, one bit easier per extra slot of gap, floored at
// E. Uses the live constants so the numbers track whatever the ledger ships.
func TestMineRequiredK(t *testing.T) {
	c := ledger.L(0).Constants
	p := c.MineMinPace
	e := c.MineFloorDifficulty
	b := e + 5 // a B comfortably above the floor

	// full B at (and below) the minimum pace
	require.EqualValues(t, b, c.MineRequiredK(b, p))
	require.EqualValues(t, b, c.MineRequiredK(b, p-1))
	// one bit easier per extra slot of gap
	require.EqualValues(t, b-1, c.MineRequiredK(b, p+1))
	require.EqualValues(t, b-3, c.MineRequiredK(b, p+3))
	// clamped at the floor: a gap of B-E slots past P reaches E, and never below
	require.EqualValues(t, e, c.MineRequiredK(b, p+(b-e)))
	require.EqualValues(t, e, c.MineRequiredK(b, p+1000))
}

// TestMineAdjustedB pins the ±1 single-gap retarget (no snap-down): faster than
// the target hardens one bit, slower eases one bit, equal holds. Even a gap far
// past the target only eases a single bit — the pace-relieved K, not a snap-down,
// provides liveness.
func TestMineAdjustedB(t *testing.T) {
	c := ledger.L(0).Constants
	predSlot := uint32(1000)
	e := c.MineFloorDifficulty
	b := e + 6

	// faster than target -> harden; at target -> hold; slower -> ease one bit
	require.EqualValues(t, b+1, c.MineAdjustedB(b, predSlot, predSlot+uint32(c.MineTargetPace)-1))
	require.EqualValues(t, b, c.MineAdjustedB(b, predSlot, predSlot+uint32(c.MineTargetPace)))
	require.EqualValues(t, b-1, c.MineAdjustedB(b, predSlot, predSlot+uint32(c.MineTargetPace)+1))
	// a huge gap still only eases one bit (no snap-down)
	require.EqualValues(t, b-1, c.MineAdjustedB(b, predSlot, predSlot+1000))
}
