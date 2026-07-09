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
// matches ledger.NewMineLock(r, b, s1, s2, s3).Bytes() across the
// zero-elided and fully-populated cases.
func TestNewMineLock_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	cases := []struct {
		r, b       uint64
		s1, s2, s3 uint32
	}{
		{0, 0, 0, 0, 0},                             // all elided
		{900_000_000_000_000, 24, 100, 50, 10},      // typical
		{500_000_000, 8, 0xFFFFFFFF, 0x00A0B0C0, 1}, // wide ring slots
	}
	for _, c := range cases {
		walletBin, err := lib.NewMineLock(c.r, c.b, c.s1, c.s2, c.s3)
		require.NoError(t, err)
		serverBin := ledger.NewMineLock(c.r, c.b, c.s1, c.s2, c.s3).Bytes()
		require.Equal(t, serverBin, walletBin, "case %+v", c)
	}
}

// TestParseMineLock_RoundTrip verifies the wallet parser decodes the
// ledger-emitted bytecode back to the same R/B/ring fields.
func TestParseMineLock_RoundTrip(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	const (
		r          = uint64(900_000_000_000_000)
		b          = uint64(24)
		s1, s2, s3 = uint32(100), uint32(50), uint32(10)
	)
	bin := ledger.NewMineLock(r, b, s1, s2, s3).Bytes()
	view, err := lib.ParseMineLock(bin)
	require.NoError(t, err)
	require.EqualValues(t, r, view.R)
	require.EqualValues(t, b, view.B)
	require.EqualValues(t, s1, view.S1)
	require.EqualValues(t, s2, view.S2)
	require.EqualValues(t, s3, view.S3)
}
