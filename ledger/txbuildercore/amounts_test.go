package txbuildercore

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDecodeAmountsVector checks that DecodeAmountsVector is the exact
// inverse of EncodeAmounts, including the trailing-zero elision: a
// vector with trailing zeros encodes the same as the truncated one, so
// the decode reflects the on-wire (truncated) shape, not the input.
func TestDecodeAmountsVector(t *testing.T) {
	cases := [][]uint64{
		{},                 // empty -> empty
		{0},                // single zero elides to empty
		{100},              // balance only
		{100, 7},           // balance + inflation
		{100, 0, 5},        // gap (inflation 0) preserved, frozen coverage set
		{100, 7, 0, 0},     // trailing zeros elided on the wire
		{1 << 40, 1 << 20}, // multi-byte trimmed uint64s
	}
	for _, in := range cases {
		got, err := DecodeAmountsVector(EncodeAmounts(in...))
		require.NoError(t, err)

		// Expected = input with trailing zeros stripped (the wire form).
		want := append([]uint64(nil), in...)
		for len(want) > 0 && want[len(want)-1] == 0 {
			want = want[:len(want)-1]
		}
		if len(want) == 0 {
			require.Empty(t, got)
		} else {
			require.Equal(t, want, got)
		}
	}
}

// TestDecodeTokenBalance round-trips an output's balance through
// EncodeTokenBalance -> output tuple -> DecodeTokenBalance.
func TestDecodeTokenBalance(t *testing.T) {
	const balance = uint64(123_456_789)
	b := NewOutputBuilder()
	b.PutConstraint(EncodeTokenBalance(balance), ConstraintIndexAmounts)
	b.PutConstraint(EncodeIndexValuesTuple([][]byte{make([]byte, 32)}), ConstraintIndexIndexValues)
	b.PutConstraint([]byte{0x80}, ConstraintIndexLock) // placeholder lock

	got, err := DecodeTokenBalance(b.Output().Bytes())
	require.NoError(t, err)
	require.Equal(t, balance, got)

	// An output whose amounts vector is an empty / all-zero balance
	// decodes to 0 rather than erroring.
	z := NewOutputBuilder()
	z.PutConstraint(EncodeTokenBalance(0), ConstraintIndexAmounts)
	z.PutConstraint([]byte{0x80}, ConstraintIndexLock)
	gotZero, err := DecodeTokenBalance(z.Output().Bytes())
	require.NoError(t, err)
	require.Equal(t, uint64(0), gotZero)
}
