package txbuildercore_test

// Byte-identity tests for the Phase-B chain helpers: chain
// origin / transition bytecode plus the 1-byte unlock-params blobs.
// Each test compares wallet-emitted bytes against the existing
// ledger.* constructor so the wallet and server agree byte-for-byte.

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestNewChainOrigin_ByteIdentity verifies the chain-origin bytecode
// matches ledger.NewChainOrigin.Bytes() for the same start slot.
func TestNewChainOrigin_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)
	for _, slot := range []uint32{0, 1, 42, 1 << 16, 0xFFFFFFFF >> 1} {
		walletBin, err := lib.NewChainOrigin(slot)
		require.NoError(t, err)
		serverBin := ledger.NewChainOrigin(slot).Bytes()
		require.Equal(t, serverBin, walletBin, "slot=%d", slot)
	}
}

// TestNewChainTransition_ByteIdentity verifies the chain-transition
// bytecode matches ledger.NewChainConstraint(...).Bytes() across both
// fully-populated and zero-counter cases.
func TestNewChainTransition_ByteIdentity(t *testing.T) {
	lib := txbuildercoreLibFromGlobal(t)

	var chainID base.ChainID
	for i := range chainID {
		chainID[i] = byte(i + 1)
	}

	// Fully populated transition.
	{
		walletBin, err := lib.NewChainTransition(
			chainID, 3, 1234,
			999_999, 88_888, 17, 5,
		)
		require.NoError(t, err)
		serverBin := ledger.NewChainConstraint(
			chainID, 3, 1234,
			999_999, 88_888, 17, 5,
		).Bytes()
		require.Equal(t, serverBin, walletBin)
	}

	// Zero-counter transition — exercises the z64/z32 trim
	// behaviour. NewChainConstraint asserts branchCounter <=
	// transitionCounter; both zero is fine.
	{
		walletBin, err := lib.NewChainTransition(
			chainID, 0, 0,
			0, 0, 0, 0,
		)
		require.NoError(t, err)
		serverBin := ledger.NewChainConstraint(
			chainID, 0, 0,
			0, 0, 0, 0,
		).Bytes()
		require.Equal(t, serverBin, walletBin)
	}
}

// TestChainUnlockParams checks each 1-byte unlock-params blob
// matches ledger.NewChainUnlockParams for the same index.
func TestChainUnlockParams(t *testing.T) {
	for _, idx := range []byte{0, 1, 7, 0x80, 0xFE, 0xFF} {
		require.Equal(t,
			ledger.NewChainUnlockParams(idx),
			txbuildercore.ChainUnlockParams(idx),
			"idx=%d", idx,
		)
	}
}

// TestFinishChainUnlockParams checks the chain-finish (no-successor)
// sentinel matches ledger's value.
func TestFinishChainUnlockParams(t *testing.T) {
	require.Equal(t, ledger.FinishChainUnlockParams, txbuildercore.FinishChainUnlockParams)
}

// TestChainLockUnlockParams checks the chainLock unlock-params blob
// matches ledger.NewChainLockUnlockParams for the same predecessor
// input index.
func TestChainLockUnlockParams(t *testing.T) {
	for _, idx := range []byte{0, 1, 13, 0xFE} {
		require.Equal(t,
			ledger.NewChainLockUnlockParams(idx),
			txbuildercore.ChainLockUnlockParams(idx),
			"idx=%d", idx,
		)
	}
}
