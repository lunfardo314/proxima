package util_test

import (
	"math"
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// TestProx pins the PROX rendering of motes, including the unit equal to
// base.PROX (util keeps its own copy to avoid importing the ledger).
func TestProx(t *testing.T) {
	require.Equal(t, "1 PROX", util.Prox(base.PROX))
	require.Equal(t, "0 PROX", util.Prox(0))
	require.Equal(t, "0.000001 PROX", util.Prox(1))
	require.Equal(t, "95 PROX", util.Prox(95*base.PROX))
	require.Equal(t, "12,345.6789 PROX", util.Prox(12_345_678_900))
	require.Equal(t, "1,000,000.1 PROX", util.Prox(1_000_000_100_000))
	require.Equal(t, "18,446,744,073,709.551615 PROX", util.Prox(math.MaxUint64))
	require.Equal(t, "-2.5 PROX", util.ProxSigned(-2_500_000))
	require.Equal(t, "2.5 PROX", util.ProxSigned(2_500_000))
	require.Equal(t, "-9,223,372,036,854.775808 PROX", util.ProxSigned(math.MinInt64))
}
