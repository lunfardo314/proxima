package txcore_test

// Phase 4 helper tests. The library is borrowed from the global ledger
// singleton (loaded by package init) so we exercise the same library
// the server side uses, and verify byte-for-byte equivalence with the
// existing ledger.* constructors.

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txcore"
	"github.com/stretchr/testify/require"
)

// init brings up the ledger singleton with a testing parameter set so
// ledger.L(...) works inside the helpers_test cases (the wallet's
// Library is built from the singleton's JSON descriptors).
func init() {
	ledger.InitWithTestingLedgerData()
}

// txcoreLibFromGlobal serialises the current ledger.L() singleton to
// JSON, parses it back, and constructs a txcore.Library. This mimics
// what the wallet does at init time (parse bundled library.json,
// build a Library) while reusing the test environment's library.
func txcoreLibFromGlobal(t *testing.T) *txcore.Library {
	t.Helper()
	lib := ledger.L(base.MaxSlot)
	jsonBytes := easyfl.ToJSON(lib.Library, true, false)
	desc, err := easyfl.ReadLibraryFromJSON(jsonBytes)
	require.NoError(t, err)
	tlib, err := txcore.NewLibrary(desc)
	require.NoError(t, err)
	return tlib
}

// TestLibrary_CompileExpression verifies CompileExpression works on
// the wallet path: parse a known base function ("concat") into
// bytecode and decompile back.
func TestLibrary_CompileExpression(t *testing.T) {
	lib := txcoreLibFromGlobal(t)
	code, err := lib.CompileExpression("concat(0x01, 0x02)")
	require.NoError(t, err)
	require.NotEmpty(t, code)

	src, err := lib.DecompileBytecode(code)
	require.NoError(t, err)
	// Decompile normalises single-byte hex literals to their decimal
	// form (0x01 → 1); just check the structure.
	require.Equal(t, "concat(1,2)", src)
}

// TestNewSigLockOutput_ByteIdentity verifies the txcore helper
// produces bytes byte-identical to the existing ledger.NewOutput +
// WithLock(SigLock) flow. The wallet and the server must agree on the
// wire form down to the last byte.
func TestNewSigLockOutput_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	var holder base.HolderID
	for i := range holder {
		holder[i] = byte(i + 1)
	}
	const amount uint64 = 1234567

	// Wallet path.
	wallet, err := txcore.NewSigLockOutput(lib, amount, holder)
	require.NoError(t, err)

	// Server path.
	server := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(amount).WithLock(ledger.SigLock(holder))
	})

	require.Equal(t, server.Bytes(), wallet.Bytes())
}

// TestNewTagAlongOutput_ByteIdentity verifies tag-along byte identity
// between wallet and server compose paths.
func TestNewTagAlongOutput_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	var sender base.HolderID
	var target base.ChainID
	for i := range sender {
		sender[i] = byte(i + 1)
	}
	for i := range target {
		target[i] = byte(i + 100)
	}
	const fee uint64 = 500

	wallet, err := txcore.NewTagAlongOutput(lib, fee, target, sender)
	require.NoError(t, err)

	server := ledger.NewTagAlongOutput(fee, target, sender)

	require.Equal(t, server.Bytes(), wallet.Bytes())
}
