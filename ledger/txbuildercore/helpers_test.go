package txbuildercore_test

// Phase 4 helper tests. The library is borrowed from the global ledger
// singleton (loaded by package init) so we exercise the same library
// the server side uses, and verify byte-for-byte equivalence with the
// existing ledger.* constructors.

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// init brings up the ledger singleton with a testing parameter set so
// ledger.L(...) works inside the helpers_test cases (the wallet's
// Library is built from the singleton's JSON descriptors).
func init() {
	ledger.InitWithTestingLedgerData()
}

// txbuildercoreLibFromGlobal serialises the current ledger.L() singleton to
// JSON, parses it back, and constructs a txbuildercore.Library. This mimics
// what the wallet does at init time (parse bundled library.json,
// build a Library) while reusing the test environment's library.
func txbuildercoreLibFromGlobal(t *testing.T) *txbuildercore.Library {
	t.Helper()
	lib := ledger.L(base.MaxSlot)
	jsonBytes := easyfl.ToJSON(lib.Library, true, false)
	desc, err := easyfl.ReadLibraryFromJSON(jsonBytes)
	require.NoError(t, err)
	tlib, err := txbuildercore.NewLibrary(desc)
	require.NoError(t, err)
	return tlib
}

// TestLibrary_CompileExpression verifies CompileExpression works on
// the wallet path: parse a known base function ("concat") into
// bytecode and decompile back.
func TestLibrary_CompileExpression(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	code, err := lib.CompileExpression("concat(0x01, 0x02)")
	require.NoError(t, err)
	require.NotEmpty(t, code)

	src, err := lib.DecompileBytecode(code)
	require.NoError(t, err)
	// Decompile normalises single-byte hex literals to their decimal
	// form (0x01 → 1); just check the structure.
	require.Equal(t, "concat(1,2)", src)
}

// TestNewSigLockOutput_ByteIdentity verifies the txbuildercore helper
// produces bytes byte-identical to the existing ledger.NewOutput +
// WithLock(SigLock) flow. The wallet and the server must agree on the
// wire form down to the last byte.
func TestNewSigLockOutput_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	var holder base.HolderID
	for i := range holder {
		holder[i] = byte(i + 1)
	}
	const amount uint64 = 1234567

	// Wallet path.
	wallet, err := txbuildercore.NewSigLockOutput(lib, amount, holder)
	require.NoError(t, err)

	// Server path.
	server := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(amount).WithLock(ledger.SigLock(holder))
	})

	require.Equal(t, server.Bytes(), wallet.Bytes())
}

// TestNewChainLockOutput_ByteIdentity verifies the txbuildercore
// helper produces bytes byte-identical to ledger.NewOutput +
// WithLock(ChainLock) for the same amount + chain id.
func TestNewChainLockOutput_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	var chainID base.ChainID
	for i := range chainID {
		chainID[i] = byte(i + 7)
	}
	const amount uint64 = 9_876_543

	wallet, err := txbuildercore.NewChainLockOutput(lib, amount, chainID)
	require.NoError(t, err)

	server := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(amount).WithLock(ledger.ChainLockFromChainID(chainID))
	})

	require.Equal(t, server.Bytes(), wallet.Bytes())
}

// TestNewTagAlongOutput_ByteIdentity verifies tag-along byte identity
// between wallet and server compose paths.
func TestNewTagAlongOutput_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	var sender base.HolderID
	var target base.ChainID
	for i := range sender {
		sender[i] = byte(i + 1)
	}
	for i := range target {
		target[i] = byte(i + 100)
	}
	const fee uint64 = 500

	wallet, err := txbuildercore.NewTagAlongOutput(lib, fee, target, sender)
	require.NoError(t, err)

	server := ledger.NewTagAlongOutput(fee, target, sender)

	require.Equal(t, server.Bytes(), wallet.Bytes())
}
