package base

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for the StringDashed / FromStringDashed round-trip format.
// Format: [s]<slot>-<tick>-<hex of 27-byte TransactionIDShort>
//   - 's' prefix iff the sequencer bit is set (covers branches too)
//   - no separator between maxOutputIndex and the hash tail — maxOutputIndex is
//     simply the first byte of the 27-byte short ID hex

func TestTransactionID_StringDashed_RoundTrip(t *testing.T) {
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
			s := txid.StringDashed()
			t.Logf("StringDashed: %s", s)

			parsed, err := TransactionIDFromStringDashed(s)
			require.NoError(t, err)
			require.Equal(t, txid, parsed, "round-trip mismatch")
		})
	}
}

func TestOutputID_StringDashed_RoundTrip(t *testing.T) {
	txid := RandomTransactionID(true, 10, T(999, 5))
	for idx := byte(0); idx <= 10; idx++ {
		oid := MustNewOutputID(txid, idx)
		s := oid.StringDashed()
		t.Logf("OutputID StringDashed: %s", s)

		parsed, err := OutputIDFromStringDashed(s)
		require.NoError(t, err)
		require.Equal(t, oid, parsed, "round-trip mismatch for index %d", idx)
	}
}

func TestTransactionIDFromStringDashed_Errors(t *testing.T) {
	// empty input
	_, err := TransactionIDFromStringDashed("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty")

	// missing fields (only slot-tick, no hash)
	_, err = TransactionIDFromStringDashed("s100-5")
	require.Error(t, err)

	// wrong hash length: hex parses but byte count is not 27
	_, err = TransactionIDFromStringDashed("s100-5-aabbcc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be")

	// invalid slot (non-numeric)
	_, err = TransactionIDFromStringDashed("sXYZ-5-" + strings.Repeat("00", TransactionIDShortLength))
	require.Error(t, err)
	require.Contains(t, err.Error(), "slot")

	// bad hex
	_, err = TransactionIDFromStringDashed("s100-5-" + strings.Repeat("zz", TransactionIDShortLength))
	require.Error(t, err)
	require.Contains(t, err.Error(), "hash hex")
}

func TestStringDashed_Prefix(t *testing.T) {
	// branch transaction: sequencer bit set, tick=0 → 's' prefix (no special 'b')
	branch := RandomTransactionID(true, 3, T(100, 0))
	require.Equal(t, byte('s'), branch.StringDashed()[0])

	// sequencer non-branch: sequencer bit set, tick>0 → 's' prefix
	seqNonBranch := RandomTransactionID(true, 3, T(100, 5))
	require.Equal(t, byte('s'), seqNonBranch.StringDashed()[0])

	// non-sequencer: no prefix, first char must be a digit
	nonSeq := RandomTransactionID(false, 3, T(100, 5))
	first := nonSeq.StringDashed()[0]
	require.True(t, first >= '0' && first <= '9', "non-seq first char must be a digit, got %q", first)
}

func TestStringDashed_NoMaxOutputIndexDash(t *testing.T) {
	// The maxOutputIndex byte must be the first hex byte of the 27-byte short, with no
	// dedicated dash-separated field. Concretely: after the leading prefix (optional 's')
	// and the slot/tick dashes, the dashed form has exactly 2 dashes — no third one.
	txid := RandomTransactionID(true, 7, T(100, 5))
	s := txid.StringDashed()
	require.Equal(t, 2, strings.Count(s, "-"), "expected exactly 2 dashes in %q", s)
}

func TestStringDashed_ShortAndVeryShort(t *testing.T) {
	// The short/very-short forms are NOT parseable (truncated); we only check that they
	// share the same prefix+slot-tick header as the full form, and that they shorten the
	// hex tail to 12 / 8 chars respectively followed by "..".
	txid := RandomTransactionID(true, 5, T(42, 3))
	full := txid.StringDashed()
	short := txid.StringDashedShort()
	vshort := txid.StringDashedVeryShort()

	// header (up to and including the second dash, e.g. "s42-3-") is identical across all three forms
	firstDash := strings.Index(full, "-")
	secondDash := firstDash + 1 + strings.Index(full[firstDash+1:], "-")
	header := full[:secondDash+1]
	require.True(t, strings.HasPrefix(short, header), "short form must keep the slot-tick header")
	require.True(t, strings.HasPrefix(vshort, header), "very-short form must keep the slot-tick header")

	// short = header + 12 hex chars (6 bytes of TransactionIDShort) + ".."
	require.Equal(t, len(header)+12+2, len(short))
	require.True(t, strings.HasSuffix(short, ".."))

	// very-short = header + 8 hex chars (4 bytes) + ".."
	require.Equal(t, len(header)+8+2, len(vshort))
	require.True(t, strings.HasSuffix(vshort, ".."))

	// non-sequencer variant: no 's' prefix at all
	nonSeq := RandomTransactionID(false, 5, T(42, 3))
	require.False(t, strings.HasPrefix(nonSeq.StringDashed(), "s"))
	require.False(t, strings.HasPrefix(nonSeq.StringDashedShort(), "s"))
	require.False(t, strings.HasPrefix(nonSeq.StringDashedVeryShort(), "s"))
}

func TestOutputID_ShortAndVeryShort(t *testing.T) {
	// Output-id short/very-short forms just append "#<idx>" to the matching txid form.
	txid := RandomTransactionID(true, 9, T(50, 7))
	oid := MustNewOutputID(txid, 3)

	require.Equal(t, txid.StringDashed()+"#3", oid.StringDashed())
	require.Equal(t, txid.StringDashedShort()+"#3", oid.StringDashedShort())
	require.Equal(t, txid.StringDashedVeryShort()+"#3", oid.StringDashedVeryShort())
}

// TestPrintAllForms is a non-assertive eyeballing test: it prints every available
// human-readable form of TransactionID and OutputID for a representative set of
// transaction shapes (branch, sequencer non-branch, non-sequencer). Run with -v
// to view the output.
func TestPrintAllForms(t *testing.T) {
	cases := []struct {
		name      string
		seqFlag   bool
		ts        LedgerTime
		maxOutIdx byte
	}{
		{"branch tx (seq, tick=0)", true, T(12345, 0), 5},
		{"sequencer non-branch", true, T(12345, 15), 10},
		{"non-sequencer", false, T(12345, 3), 7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			txid := RandomTransactionID(tc.seqFlag, tc.maxOutIdx, tc.ts)
			oid := MustNewOutputID(txid, 2)

			t.Logf("--- TransactionID forms (%s) ---", tc.name)
			t.Logf("  String:                 %s", txid.String())
			t.Logf("  StringHex:              %s", txid.StringHex())
			t.Logf("  StringShort:            %s", txid.StringShort())
			t.Logf("  StringVeryShort:        %s", txid.StringVeryShort())
			t.Logf("  StringDashed:           %s", txid.StringDashed())
			t.Logf("  StringDashedShort:      %s", txid.StringDashedShort())
			t.Logf("  StringDashedVeryShort:  %s", txid.StringDashedVeryShort())
			t.Logf("  AsFileName:             %s", txid.AsFileName())
			t.Logf("  AsFileNameShort:        %s", txid.AsFileNameShort())

			t.Logf("--- OutputID forms (idx=%d) ---", oid.Index())
			t.Logf("  String:                 %s", oid.String())
			t.Logf("  StringHex:              %s", oid.StringHex())
			t.Logf("  StringShort:            %s", oid.StringShort())
			t.Logf("  StringVeryShort:        %s", oid.StringVeryShort())
			t.Logf("  StringDashed:           %s", oid.StringDashed())
			t.Logf("  StringDashedShort:      %s", oid.StringDashedShort())
			t.Logf("  StringDashedVeryShort:  %s", oid.StringDashedVeryShort())
		})
	}
}
