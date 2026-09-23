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
// matches ledger.NewMineLock(r, b, c).Bytes() across the zero-elided and
// fully-populated cases.
func TestNewMineLock_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	cases := []struct {
		r, b, c uint64
	}{
		{0, 0, 0},                    // all elided
		{900_000_000_000_000, 24, 3}, // typical
		{500_000_000, 56, 7},         // wide R, ceiling difficulty, a run about to harden
	}
	for _, c := range cases {
		walletBin, err := lib.NewMineLock(c.r, c.b, c.c)
		require.NoError(t, err)
		serverBin := ledger.NewMineLock(c.r, c.b, c.c).Bytes()
		require.Equal(t, serverBin, walletBin, "case %+v", c)
	}
}

// TestParseMineLock_RoundTrip verifies the wallet parser decodes the
// ledger-emitted bytecode back to the same R/B/C fields.
func TestParseMineLock_RoundTrip(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	const (
		r = uint64(900_000_000_000_000)
		b = uint64(24)
		c = uint64(5)
	)
	bin := ledger.NewMineLock(r, b, c).Bytes()
	view, err := lib.ParseMineLock(bin)
	require.NoError(t, err)
	require.EqualValues(t, r, view.R)
	require.EqualValues(t, b, view.B)
	require.EqualValues(t, c, view.C)
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

// TestMineRetarget pins the asymmetric retarget: a full slot (gap 1) counts one
// more, the count reaching MineHardenAfter hardens one bit and restarts it; an
// empty slot eases one bit per slot of gap beyond the first, floored, and
// leaves the count alone. Both are held while the predecessor is genesis.
func TestMineRetarget(t *testing.T) {
	c := ledger.L(0).Constants
	predSlot := uint32(1000)
	e := c.MineFloorDifficulty
	b := e + 6
	k := c.MineHardenAfter

	// genesis predecessor: held
	gb, gc := c.MineRetarget(b, 3, 0, 1)
	require.EqualValues(t, b, gb)
	require.EqualValues(t, 3, gc)
	// a full slot counts; the run completing hardens and resets
	nb, nc := c.MineRetarget(b, 0, predSlot, predSlot+1)
	require.EqualValues(t, b, nb)
	require.EqualValues(t, 1, nc)
	nb, nc = c.MineRetarget(b, k-1, predSlot, predSlot+1)
	require.EqualValues(t, b+1, nb)
	require.EqualValues(t, 0, nc)
	// clamped at the ceiling
	nb, _ = c.MineRetarget(c.MineMaxDifficulty, k-1, predSlot, predSlot+1)
	require.EqualValues(t, c.MineMaxDifficulty, nb)
	// empty slots ease one bit each and keep the count
	nb, nc = c.MineRetarget(b, 2, predSlot, predSlot+2)
	require.EqualValues(t, b-1, nb)
	require.EqualValues(t, 2, nc)
	nb, _ = c.MineRetarget(b, 2, predSlot, predSlot+4)
	require.EqualValues(t, b-3, nb)
	// floored
	nb, _ = c.MineRetarget(b, 2, predSlot, predSlot+1000)
	require.EqualValues(t, e, nb)
}
