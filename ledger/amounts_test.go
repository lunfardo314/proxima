package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Encoding of the amounts vector. The vector is
//
//	[0] token balance, [1] inflation, [2] frozen-coverage bound, [3+i] frozen
//	coverage at epoch offset i
//
// and NewAmounts encodes it by deriving the bound from the frozen-coverage
// values and then dropping every cell the decoder reconstructs on its own:
// cells past the end of the tuple read as 0, and inside the frozen-coverage
// region the last encoded cell repeats up to the bound. Frozen coverage is
// constant over a delegation's frozen span, so that span costs two cells (the
// bound and the value) whatever its length - which is the point of the bound.
// Without it, a span shorter than the maximum would end in zeros that have to
// be spelled out, and spelling them out forces out every cell of the run
// before them as well.
//
// The pairs below are (logical vector, expected number of encoded cells).
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
	// head builds the non-coverage part of a logical vector: balance, inflation
	// and the bound cell, which callers leave to NewAmounts
	head := func(balance, inflation int64) []int64 {
		return []int64{balance, inflation, 0}
	}

	testCases := []struct {
		name      string
		logical   []int64
		wantCells int
	}{
		// all-zero vector encodes as the empty tuple
		{"empty", nil, 0},
		{"all zero", repeat(0, 3+maxDepth), 0},
		// ordinary output: balance only, no padding cell for the bound
		{"balance only", []int64{1_000_000}, 1},
		{"balance and inflation", []int64{1_000_000, 42}, 2},
		// trailing zeros below the frozen-coverage region are still dropped
		{"zero inflation dropped", []int64{1_000_000, 0}, 1},
		// no frozen coverage: the bound cell itself is dropped too
		{"no frozen coverage", head(1_000_000, 42), 2},
		// a delegation frozen to the maximum depth: bound plus one value
		{"max depth frozen", concat(head(1_000_000, 42), repeat(777, maxDepth)), 4},
		// the case the bound exists for: a partial freeze costs exactly the same
		{"partial freeze", concat(head(1_000_000, 42), repeat(777, 10), repeat(0, maxDepth-10)), 4},
		// negative deltas (an askstop-produced output) encode the same way
		{"partial freeze negative", concat(head(1_000_000, 0), repeat(-777, 10), repeat(0, maxDepth-10)), 4},
		// a varied vector (a sequencer aggregate) cannot compress beyond its last
		// change, but its zero tail still costs nothing
		{"staircase", concat(head(1_000_000, 42), []int64{900, 800, 700}, repeat(0, maxDepth-3)), 6},
		// a zero inside the covered span is a value like any other and stays
		{"gap inside the span", concat(head(1_000_000, 0), []int64{900, 0, 700}, repeat(0, maxDepth-3)), 6},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAmounts(tc.logical...)
			require.EqualValues(t, tc.wantCells, a.NumElements(), "encoded cell count")

			// every logical element must read back unchanged
			for i, want := range tc.logical {
				var got int64
				switch {
				case i == int(AmountIndexFrozenCoverageBound):
					continue // derived, not what the caller passed
				case i >= int(AmountIndexFrozenCoverage):
					got = a.FrozenCoverageAt(byte(i - int(AmountIndexFrozenCoverage)))
				default:
					got = a.Amount(byte(i))
				}
				require.EqualValues(t, want, got, "element %d", i)
			}
			// and everything past the covered span reads as 0
			require.EqualValues(t, 0, a.FrozenCoverageAt(maxDepth), "past the bound")
		})
	}
}

// The bound must equal the number of epochs actually covered: it is what stops
// the repetition of the last encoded cell, so a wrong one would silently
// stretch or truncate the coverage of a delegation.
func TestAmountsFrozenCoverageBound(t *testing.T) {
	const maxDepth = 60

	v := make([]int64, int(AmountIndexFrozenCoverage)+maxDepth)
	v[AmountIndexTokenBalance] = 1_000_000
	for i := 0; i < 10; i++ {
		v[int(AmountIndexFrozenCoverage)+i] = 777
	}
	a := NewAmounts(v...)

	require.EqualValues(t, 10, a.FrozenCoverageBound())
	require.False(t, a.IsFrozenCoverageZero())
	for i := 0; i < 10; i++ {
		require.EqualValues(t, 777, a.FrozenCoverageAt(byte(i)), "epoch %d", i)
	}
	for i := 10; i < maxDepth; i++ {
		require.EqualValues(t, 0, a.FrozenCoverageAt(byte(i)), "epoch %d", i)
	}

	// an output with no frozen coverage at all
	plain := NewAmounts(1_000_000)
	require.EqualValues(t, 0, plain.FrozenCoverageBound())
	require.True(t, plain.IsFrozenCoverageZero())
}

// AddToVector must expand the collapsed run over every epoch the bound covers,
// and must not add the bound itself: the per-index sums it feeds are compared
// against per-epoch coverage totals, and a bound summed as if it were an amount
// would corrupt the epoch it landed on.
func TestAmountsAddToVector(t *testing.T) {
	const maxDepth = 60

	frozen := make([]int64, int(AmountIndexFrozenCoverage)+maxDepth)
	frozen[AmountIndexTokenBalance] = 1_000_000
	for i := 0; i < 10; i++ {
		frozen[int(AmountIndexFrozenCoverage)+i] = 777
	}
	a := NewAmounts(frozen...)
	require.EqualValues(t, 4, a.NumElements(), "bound plus one frozen-coverage cell")

	sum := make([]int64, int(AmountIndexFrozenCoverage)+maxDepth)
	require.False(t, a.AddToVector(sum))

	require.EqualValues(t, 1_000_000, sum[AmountIndexTokenBalance])
	require.EqualValues(t, 0, sum[AmountIndexInflation])
	require.EqualValues(t, 0, sum[AmountIndexFrozenCoverageBound], "the bound is not an amount")
	for i := 0; i < maxDepth; i++ {
		want := int64(0)
		if i < 10 {
			want = 777
		}
		require.EqualValues(t, want, sum[int(AmountIndexFrozenCoverage)+i], "frozen coverage at %d", i)
	}
}
