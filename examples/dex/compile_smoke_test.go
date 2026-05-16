package dex

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBundleCompiles asserts the dex.easyfl source compiles and all three
// public entries are discoverable. Runs at package init via GetBins().
func TestBundleCompiles(t *testing.T) {
	b := GetBins()
	require.NotEmpty(t, b.Bin, "compiled binary must be non-empty")
	require.NotZero(t, b.Hash, "compiled hash must be set")
	require.True(t, b.SellOrderIdx >= 0, "sellOrder entry index")
	require.True(t, b.BuyOrderIdx >= 0, "buyOrder entry index")
	require.True(t, b.RandomizeConsumptionIdx >= 0, "randomizeConsumption entry index")
	require.NotEqual(t, b.SellOrderIdx, b.BuyOrderIdx,
		"sellOrder and buyOrder must be distinct entries")
	t.Logf("dex binary size: %d bytes, hash: %x", len(b.Bin), b.Hash)
	t.Logf("entries: sellOrder=%d buyOrder=%d randomizeConsumption=%d",
		b.SellOrderIdx, b.BuyOrderIdx, b.RandomizeConsumptionIdx)
}

// TestLockBytecodeCompiles asserts every helper that builds a callRedeemer
// bytecode for the lock or for an optional extra constraint compiles cleanly
// against typical PoC parameters.
func TestLockBytecodeCompiles(t *testing.T) {
	bc, err := SellOrderLockBytecode(100, 24)
	require.NoError(t, err)
	require.NotEmpty(t, bc)

	bc, err = BuyOrderLockBytecode(10, 100, 24)
	require.NoError(t, err)
	require.NotEmpty(t, bc)

	bc, err = RandomizeConsumptionBytecode(4)
	require.NoError(t, err)
	require.NotEmpty(t, bc)

	bc, err = RedeemScriptConstraint()
	require.NoError(t, err)
	require.NotEmpty(t, bc)
}
