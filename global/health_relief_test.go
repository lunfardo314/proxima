package global

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
)

// TestHealthRelief covers the coordinated relaxation of the healthy-branch threshold: it applies
// to branches inside the configured slot window and to nothing else, so a node judges a branch by
// the same rule wherever health is checked — issue gates, attacher acceptance and LRB selection.
func TestHealthRelief(t *testing.T) {
	ledger.InitWithTestingLedgerData()
	// the window is process-global; leave it as found so it cannot leak into other tests
	t.Cleanup(func() { healthReliefWindow.Store(nil) })

	ledgerFraction := FractionHealthyBranch()
	require.Equal(t, ledgerFraction, FractionHealthyBranchAt(1000), "no relief configured: the ledger fraction applies everywhere")

	// a relief fraction must be a proper fraction inside a non-empty slot range
	require.Error(t, SetHealthRelief(200, 100, Fraction{Numerator: 4, Denominator: 12}))
	require.Error(t, SetHealthRelief(100, 200, Fraction{Numerator: 4, Denominator: 0}))
	require.Error(t, SetHealthRelief(100, 200, Fraction{Numerator: 12, Denominator: 12}))
	require.NoError(t, SetHealthRelief(100, 200, Fraction{Numerator: 4, Denominator: 12}))

	relief := Fraction{Numerator: 4, Denominator: 12}
	require.Equal(t, ledgerFraction, FractionHealthyBranchAt(99), "before the window")
	require.Equal(t, relief, FractionHealthyBranchAt(100), "first slot of the window")
	require.Equal(t, relief, FractionHealthyBranchAt(150))
	require.Equal(t, relief, FractionHealthyBranchAt(200), "last slot of the window")
	require.Equal(t, ledgerFraction, FractionHealthyBranchAt(201), "after the window")

	from, to, fraction, ok := HealthRelief()
	require.True(t, ok)
	require.EqualValues(t, 100, from)
	require.EqualValues(t, 200, to)
	require.Equal(t, relief, fraction)

	// A branch whose verdict differs under the two fractions, to show which one is applied where.
	// Note the direction: the test ledger relaxes its own fraction (0/1, everything healthy), so
	// here the relief fraction is the stricter of the two — in production it is the looser one.
	// What the test pins down is the slot at which each fraction takes over, not their order.
	const supply = uint64(12_000)
	const coverageDelta = uint64(3_000) // 3/12 of supply: below the relief fraction, above the test ledger's
	require.True(t, IsHealthyCoverageDelta(coverageDelta, supply, ledgerFraction))
	require.False(t, IsHealthyCoverageDelta(coverageDelta, supply, relief))

	require.False(t, IsHealthyBranchAt(150, coverageDelta, supply), "inside the window: judged by the relief fraction")
	require.True(t, IsHealthyBranchAt(99, coverageDelta, supply), "before the window: judged by the ledger fraction")
	require.True(t, IsHealthyBranchAt(201, coverageDelta, supply), "after the window: judged by the ledger fraction")
}
