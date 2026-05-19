package txcore_test

// Byte-identity tests for the Phase-D native-token wallet helpers:
// foundry + token + tokenAmount bytecode and the
// AppendTokenAmountToOutput composer (which mirrors the server-side
// OutputBuilder.WithTokenAmount byte-for-byte, including the
// compound-controller-index-value side effect on slot 1).

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txcore"
	"github.com/stretchr/testify/require"
)

// fixedTag returns a deterministic 32-byte ChainID used as a foundry
// tag across the native-token tests.
func fixedTag() base.ChainID {
	var tag base.ChainID
	for i := range tag {
		tag[i] = byte(i + 50)
	}
	return tag
}

// TestNewFoundryBytecode_ByteIdentity exercises the 1-arg foundry(z64/supply)
// bytecode across the z64 trim boundary.
func TestNewFoundryBytecode_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)
	for _, supply := range []uint64{0, 1, 255, 256, 1_000_000, 1 << 32, ^uint64(0)} {
		walletBin, err := lib.NewFoundryBytecode(supply)
		require.NoError(t, err)
		serverBin := ledger.NewFoundry(supply).Bytes()
		require.Equal(t, serverBin, walletBin, "supply=%d", supply)
	}
}

// TestTokenSentinel_ByteIdentity verifies the pure-conservation
// token(tag, 0xFF) form matches the ledger helper.
func TestTokenSentinel_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)
	tag := fixedTag()
	walletBin, err := lib.TokenSentinel(tag)
	require.NoError(t, err)
	serverBin := ledger.TokenSentinelBytecode(tag)
	require.Equal(t, serverBin, walletBin)
}

// TestTokenFoundry_ByteIdentity covers the foundry-transit form for
// both a concrete index and the FoundryIdxNone sentinel (which must
// match TokenSentinelBytecode).
func TestTokenFoundry_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)
	tag := fixedTag()
	for _, idx := range []byte{0, 3, 0x7F, 0xFE, txcore.FoundryIdxNone} {
		walletBin, err := lib.TokenFoundry(tag, idx)
		require.NoError(t, err)
		serverBin := ledger.TokenFoundryBytecode(tag, idx)
		require.Equal(t, serverBin, walletBin, "idx=%d", idx)
	}
}

// TestNewTokenAmountBytecode_ByteIdentity covers a few token-amount
// values across the z64 trim boundary.
func TestNewTokenAmountBytecode_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)
	tag := fixedTag()
	for _, amount := range []uint64{1, 255, 1_000_000, ^uint64(0)} {
		walletBin, err := lib.NewTokenAmountBytecode(tag, amount)
		require.NoError(t, err)
		serverBin := ledger.NewTokenAmount(tag, amount).Bytes()
		require.Equal(t, serverBin, walletBin, "amount=%d", amount)
	}
}

// TestAppendTokenAmountToOutput_ByteIdentity is the end-to-end check:
// compose a sigLock output via the wallet builder (amounts, slot-1
// controller, lock, then AppendTokenAmountToOutput) and compare bytes
// against ledger.NewOutput(o.WithAmounts(...).WithLock(sig).WithTokenAmount(tag, amt)).
// This verifies the compound-controller-index-value side effect lines
// up — slot 1 of the resulting tuple must hold the 32-byte controller
// at index 0 and the 64-byte `controller||tag` at index 1.
func TestAppendTokenAmountToOutput_ByteIdentity(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	var holder base.HolderID
	for i := range holder {
		holder[i] = byte(i + 1)
	}
	tag := fixedTag()
	const (
		amount    uint64 = 5_000_000
		tokenQty  uint64 = 123_456
	)

	// Wallet path — mirror NewSigLockOutput's setup, then append the
	// tokenAmount via the wallet helper.
	b := txcore.NewOutputBuilder()
	b.PutConstraint(txcore.EncodeTokenBalance(amount), txcore.ConstraintIndexAmounts)
	b.PutConstraint(txcore.EncodeIndexValuesTuple([][]byte{holder[:]}), txcore.ConstraintIndexIndexValues)
	sigLockBin, err := lib.CompileExpression("sigLock")
	require.NoError(t, err)
	b.PutConstraint(sigLockBin, txcore.ConstraintIndexLock)
	require.NoError(t, lib.AppendTokenAmountToOutput(b, tag, tokenQty))
	walletBytes := b.Output().Bytes()

	// Server path — single fluent chain.
	server := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(ledger.SigLock(holder)).WithTokenAmount(tag, tokenQty)
	})

	require.Equal(t, server.Bytes(), walletBytes)
}

// TestAppendTokenAmountToOutput_DedupCompound verifies the
// compound-index-value entry is added exactly once even if the wallet
// appends two tokenAmount constraints for the same tag. This mirrors
// the server's dedup in OutputBuilder.addCompoundIndexValue.
func TestAppendTokenAmountToOutput_DedupCompound(t *testing.T) {
	lib := txcoreLibFromGlobal(t)

	var holder base.HolderID
	for i := range holder {
		holder[i] = byte(i + 1)
	}
	tag := fixedTag()

	// Wallet path — append twice.
	b := txcore.NewOutputBuilder()
	b.PutConstraint(txcore.EncodeTokenBalance(1_000), txcore.ConstraintIndexAmounts)
	b.PutConstraint(txcore.EncodeIndexValuesTuple([][]byte{holder[:]}), txcore.ConstraintIndexIndexValues)
	sigLockBin, err := lib.CompileExpression("sigLock")
	require.NoError(t, err)
	b.PutConstraint(sigLockBin, txcore.ConstraintIndexLock)
	require.NoError(t, lib.AppendTokenAmountToOutput(b, tag, 100))
	require.NoError(t, lib.AppendTokenAmountToOutput(b, tag, 200))
	walletBytes := b.Output().Bytes()

	// Server path — same: two WithTokenAmount calls with the same tag.
	server := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(1_000)).WithLock(ledger.SigLock(holder)).
			WithTokenAmount(tag, 100).
			WithTokenAmount(tag, 200)
	})

	require.Equal(t, server.Bytes(), walletBytes)
}
