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
