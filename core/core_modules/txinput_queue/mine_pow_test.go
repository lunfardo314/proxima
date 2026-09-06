package txinput_queue

import (
	"math/bits"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMinePoWMeetsK pins the proof-of-work predicate used by the fair-launch mine
// admission floor (audit FNET-4). It mirrors _minePoWOK in def/lock_mine.easyfl:
// a value passes difficulty k when its low k bits are zero. Cross-checked against
// the trailing-zero-bit count that the miner and mine_verify compute.
func TestMinePoWMeetsK(t *testing.T) {
	cases := []struct {
		v    uint64
		k    uint64
		want bool
	}{
		{v: 0, k: 0, want: true},
		{v: 0, k: 10, want: true},    // all-zero passes any difficulty
		{v: 1, k: 0, want: true},     // difficulty 0 accepts anything
		{v: 1, k: 1, want: false},    // low bit set, needs 1 zero bit
		{v: 0b100, k: 2, want: true}, // low 2 bits zero
		{v: 0b100, k: 3, want: false},
		{v: 1 << 10, k: 10, want: true},
		{v: 1 << 10, k: 11, want: false},
		{v: 0xFFFFFFFFFFFFFC00, k: 10, want: true}, // exactly 10 trailing zeros
		{v: 0xFFFFFFFFFFFFFC00, k: 11, want: false},
	}
	for _, c := range cases {
		require.Equalf(t, c.want, minePoWMeetsK(c.v, c.k), "v=%#x k=%d", c.v, c.k)
	}
}

// TestMinePoWMeetsKMatchesTrailingZeros verifies the shift predicate agrees with
// a trailing-zero-bit count for every difficulty up to 64, over a spread of
// values — the property the ledger relies on (K trailing zero bits).
func TestMinePoWMeetsKMatchesTrailingZeros(t *testing.T) {
	values := []uint64{0, 1, 0x80, 0x1234567890ABCDEF, 1 << 33, 0xFFFFFFFFFFFFFFFF, 0xFF00}
	for _, v := range values {
		tz := uint64(bits.TrailingZeros64(v)) // TrailingZeros64(0) == 64
		for k := uint64(0); k <= 64; k++ {
			require.Equalf(t, k <= tz, minePoWMeetsK(v, k), "v=%#x k=%d tz=%d", v, k, tz)
		}
	}
}
