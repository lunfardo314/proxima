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

// TestMineRequiredK pins the stuck-chain relief valve: K == B for any gap up to
// the relief pace, then one bit of relief per extra slot down to the floor. Uses
// the live constants so the numbers track whatever the ledger ships.
func TestMineRequiredK(t *testing.T) {
	c := ledger.L(0).Constants
	rp := c.MineReliefPace
	e := c.MineFloorDifficulty
	b := e + 5 // a B comfortably above the floor

	// no relief at or below the relief pace
	require.EqualValues(t, b, c.MineRequiredK(b, 1))
	require.EqualValues(t, b, c.MineRequiredK(b, c.MineTargetPace))
	require.EqualValues(t, b, c.MineRequiredK(b, rp))
	// one bit of relief per slot beyond the relief pace
	require.EqualValues(t, b-1, c.MineRequiredK(b, rp+1))
	require.EqualValues(t, b-3, c.MineRequiredK(b, rp+3))
	// clamped at the floor: relief == B-E reaches the floor, and never below
	require.EqualValues(t, e, c.MineRequiredK(b, rp+(b-e)))
	require.EqualValues(t, e, c.MineRequiredK(b, rp+1000))
}

// TestMineAdjustedBReliefSnapDown: when the gap exceeds the relief pace the
// retarget snaps B down to the difficulty that was actually solvable (the relieved
// K), rather than easing a single bit — one-transit recovery from an overshoot.
func TestMineAdjustedBReliefSnapDown(t *testing.T) {
	c := ledger.L(0).Constants
	rp := c.MineReliefPace
	e := c.MineFloorDifficulty
	predSlot := uint32(1000)
	b := e + 6

	// gap well past the relief pace: B snaps to MineRequiredK, not b-1
	gap := rp + 4
	require.EqualValues(t, c.MineRequiredK(b, gap), c.MineAdjustedB(b, predSlot, predSlot+uint32(gap)))
	require.EqualValues(t, b-4, c.MineAdjustedB(b, predSlot, predSlot+uint32(gap)))
	// a normal slow gap (above target, below relief) only eases one bit
	require.EqualValues(t, b-1, c.MineAdjustedB(b, predSlot, predSlot+uint32(c.MineTargetPace)+1))
}
