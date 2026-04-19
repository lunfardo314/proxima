package base

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for the experimental String2/FromString2 round-trip format

func TestTransactionID_String2_RoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		seqFlag   bool
		tick      byte
		maxOutIdx byte
	}{
		{"branch tx", true, 0, 5},
		{"sequencer non-branch", true, 15, 10},
		{"non-sequencer tx", false, 3, 0},
		{"max output index 255", false, 7, 255},
		{"tick 0 non-seq", false, 0, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			txid := RandomTransactionID(tc.seqFlag, tc.maxOutIdx, T(12345, tc.tick))
			s := txid.String2()
			t.Logf("String2: %s", s)

			parsed, err := TransactionIDFromString2(s)
			require.NoError(t, err)
			require.Equal(t, txid, parsed, "round-trip mismatch")
		})
	}
}

func TestOutputID_String2_RoundTrip(t *testing.T) {
	txid := RandomTransactionID(true, 10, T(999, 5))
	for idx := byte(0); idx <= 10; idx++ {
		oid := MustNewOutputID(txid, idx)
		s := oid.String2()
		t.Logf("OutputID String2: %s", s)

		parsed, err := OutputIDFromString2(s)
		require.NoError(t, err)
		require.Equal(t, oid, parsed, "round-trip mismatch for index %d", idx)
	}
}

func TestTransactionIDFromString2_Errors(t *testing.T) {
	// branch prefix with non-zero tick (26 bytes = 52 hex chars)
	_, err := TransactionIDFromString2("b100-5-3-" + "00112233445566778899aabbccddeeff00112233aabbccdd0011")
	require.Error(t, err)
	require.Contains(t, err.Error(), "branch prefix")

	// invalid prefix
	_, err = TransactionIDFromString2("x100-0-3-0011223344")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid prefix")

	// too few fields
	_, err = TransactionIDFromString2("t100-5-abcdef")
	require.Error(t, err)

	// wrong hash length
	_, err = TransactionIDFromString2("t100-5-3-aabbcc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "hash must be")
}

func TestString2_Format(t *testing.T) {
	// verify prefix characters
	branch := RandomTransactionID(true, 3, T(100, 0))
	require.Equal(t, byte('b'), branch.String2()[0])

	seqNonBranch := RandomTransactionID(true, 3, T(100, 5))
	require.Equal(t, byte('s'), seqNonBranch.String2()[0])

	nonSeq := RandomTransactionID(false, 3, T(100, 5))
	require.Equal(t, byte('t'), nonSeq.String2()[0])
}
