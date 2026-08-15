package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Encoding of the amounts vector. NewAmounts drops every trailing cell that the
// decoding rule reconstructs on its own, and Amount reads it back:
//   - cells below AmountIndexFrozenCoverage (token balance, inflation) are 0
//     past the end of the tuple, as they always were;
//   - from AmountIndexFrozenCoverage on, the last encoded cell repeats to the
//     end, so a constant tail costs one cell instead of one per epoch;
//   - a tuple with no cell in that region means all-zero frozen coverage.
//
// The pairs below are (logical vector, expected number of encoded cells). What
// matters is that every logical vector survives the round trip while the common
// shapes - an ordinary output, and a delegation frozen to the maximum depth -
// stay small.
func TestAmountsEncoding(t *testing.T) {
	const maxDepth = 60 // the fixed freeze depth this encoding is sized for

	repeat := func(v int64, n int) []int64 {
		ret := make([]int64, n)
		for i := range ret {
			ret[i] = v
		}
		return ret
	}
	concat := func(vv ...[]int64) []int64 {
		var ret []int64
		for _, v := range vv {
			ret = append(ret, v...)
		}
		return ret
	}

	testCases := []struct {
		name      string
		logical   []int64
		wantCells int
	}{
		// all-zero vector encodes as the empty tuple
		{"empty", nil, 0},
		{"all zero", repeat(0, maxDepth), 0},
		// ordinary output: balance only. Must NOT need a padding cell - the
		// repeat rule starts above the inflation cell, so this is unchanged.
		{"balance only", []int64{1_000_000}, 1},
		// chain output with inflation and no frozen coverage: also unchanged,
		// because index 2 is still past the end and reads 0.
		{"balance and inflation", []int64{1_000_000, 42}, 2},
		// trailing zeros below the frozen-coverage region are still dropped
		{"zero inflation dropped", []int64{1_000_000, 0}, 1},
		// the case the rule exists for: a delegation frozen to the maximum depth
		// carries the same frozen coverage in every epoch, and collapses to one
		// cell instead of maxDepth of them
		{"max depth frozen", concat([]int64{1_000_000, 42}, repeat(777, maxDepth)), 3},
		// negative deltas (an askstop-produced output) compress the same way
		{"max depth negative", concat([]int64{1_000_000, 0}, repeat(-777, maxDepth)), 3},
		// a partial freeze needs the explicit 0 that stops the repetition
		{"partial freeze", concat([]int64{1_000_000, 42}, repeat(777, 10), repeat(0, maxDepth-10)), 13},
		// a varied tail (a sequencer aggregate) cannot compress beyond its last change
		{"staircase", []int64{1_000_000, 42, 900, 800, 700, 0}, 6},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAmounts(tc.logical...)
			require.EqualValues(t, tc.wantCells, a.NumElements(), "encoded cell count")

			// every logical element must read back unchanged, including the
			// implied tail past the end of the tuple
			for i, want := range tc.logical {
				require.EqualValues(t, want, a.Amount(byte(i)), "element %d", i)
			}
			// and reading past the logical vector keeps yielding its last value
			// (0 when the vector never reached the frozen-coverage region)
			if n := len(tc.logical); n > 0 && n < 255 {
				want := int64(0)
				if n > int(AmountIndexFrozenCoverage) {
					want = tc.logical[n-1]
				}
				require.EqualValues(t, want, a.Amount(byte(n)), "past the end")
			}
		})
	}
}

// AddToVector must add the repeating tail at every index, not only where the
// tuple happens to have a cell - otherwise summing a compressed delegation
// vector into a sequencer total would silently lose everything after the first
// frozen-coverage cell.
func TestAmountsAddToVectorReadsRepeatingTail(t *testing.T) {
	const maxDepth = 60

	frozen := make([]int64, 2+maxDepth)
	frozen[0] = 1_000_000
	for i := 2; i < len(frozen); i++ {
		frozen[i] = 777
	}
	a := NewAmounts(frozen...)
	require.EqualValues(t, 3, a.NumElements(), "compressed to one frozen-coverage cell")

	sum := make([]int64, 2+maxDepth)
	require.False(t, a.AddToVector(sum))

	require.EqualValues(t, 1_000_000, sum[0])
	require.EqualValues(t, 0, sum[1])
	for i := 2; i < len(sum); i++ {
		require.EqualValues(t, 777, sum[i], "frozen coverage at %d", i)
	}
}
