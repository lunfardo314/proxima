package util

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLinePrefixWriter pins the contract a host relies on when it embeds a
// process whose output it shows beside its own: every line gets the prefix,
// a multi-line write is prefixed per line, and a write ending mid-line is
// continued by the next write without a second prefix.
func TestLinePrefixWriter(t *testing.T) {
	var buf bytes.Buffer
	w := NewLinePrefixWriter(&buf, "[x] ")
	_, err := w.Write([]byte("one\n"))
	require.NoError(t, err)
	_, err = w.Write([]byte("two\nthree\n"))
	require.NoError(t, err)
	_, err = w.Write([]byte("fo"))
	require.NoError(t, err)
	_, err = w.Write([]byte("ur\n"))
	require.NoError(t, err)
	require.Equal(t, "[x] one\n[x] two\n[x] three\n[x] four\n", buf.String())
}
