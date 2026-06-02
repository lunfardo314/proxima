package txbuildercore_test

// Byte-identity tests for the wallet-side foundry / tokenAmount
// constraint parsers. They let proxi node foundry {mint,burn,retire}
// avoid the singleton-bound ledger.FoundryFromBytes /
// TokenAmountFromBytes entry points.

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestParseFoundryBytecode_Parity emits a few foundry bytecodes via
// the wallet helper, parses them back with the wallet parser, and
// cross-checks against ledger.FoundryFromBytes (the singleton-bound
// server-side parser).
func TestParseFoundryBytecode_Parity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	for _, supply := range []uint64{0, 1, 1_000, 1_000_000_000, 1<<63 - 1} {
		walletBin, err := lib.NewFoundryBytecode(supply)
		require.NoError(t, err)

		// Wallet parse.
		walletView, err := lib.ParseFoundryBytecode(walletBin)
		require.NoError(t, err)
		require.Equal(t, supply, walletView.Supply)

		// Server parse — byte-identical input must produce identical supply.
		serverF, err := ledger.FoundryFromBytes(walletBin)
		require.NoError(t, err)
		require.Equal(t, serverF.Supply, walletView.Supply)
	}
}

// TestParseFoundryBytecode_WrongSymbol rejects bytecode whose symbol
// is not "foundry".
func TestParseFoundryBytecode_WrongSymbol(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	// Compile any other constraint we can recognise — sigLock is 0-arg
	// and clearly distinct from foundry.
	bin, err := lib.CompileExpression("sigLock")
	require.NoError(t, err)
	_, err = lib.ParseFoundryBytecode(bin)
	require.Error(t, err)
}

// TestParseTokenAmountBytecode_Parity round-trips a few tokenAmount
// bytecodes and cross-checks against ledger.TokenAmountFromBytes.
func TestParseTokenAmountBytecode_Parity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	var tag base.ChainID
	for i := range tag {
		tag[i] = byte(i + 7)
	}
	for _, amount := range []uint64{1, 100, 1_000_000, 1 << 40} {
		walletBin, err := lib.NewTokenAmountBytecode(tag, amount)
		require.NoError(t, err)

		walletView, err := lib.ParseTokenAmountBytecode(walletBin)
		require.NoError(t, err)
		require.Equal(t, tag, walletView.Tag)
		require.Equal(t, amount, walletView.Amount)

		serverT, err := ledger.TokenAmountFromBytes(walletBin)
		require.NoError(t, err)
		require.Equal(t, serverT.Tag, walletView.Tag)
		require.Equal(t, serverT.Amount, walletView.Amount)
	}
}

// TestParseTokenAmountBytecode_ZeroRejected mirrors the server-side
// guard: a tokenAmount with amount==0 has no useful semantics and
// the parser rejects it.
func TestParseTokenAmountBytecode_ZeroRejected(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	var tag base.ChainID
	for i := range tag {
		tag[i] = byte(i + 1)
	}
	// Emit a zero-amount bytecode (NewTokenAmountBytecode doesn't
	// reject it — only the parsers do).
	bin, err := lib.NewTokenAmountBytecode(tag, 0)
	require.NoError(t, err)
	_, err = lib.ParseTokenAmountBytecode(bin)
	require.Error(t, err)
	// Server-side parser agrees.
	_, err = ledger.TokenAmountFromBytes(bin)
	require.Error(t, err)
}
