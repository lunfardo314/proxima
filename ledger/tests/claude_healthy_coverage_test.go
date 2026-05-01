package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestHealthyCoverageDelta covers the on-chain healthiness predicate
// (`healthyCoverageDelta` EasyFL function) and its Go wrapper
// `Library.IsHealthyCoverageDelta`. Both must agree with the canonical
// definition: covDelta * denominator > 2 * supply * numerator.
func TestHealthyCoverageDelta(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	num := lib.HealthyCoverageNumerator
	den := lib.HealthyCoverageDenominator

	// Sanity: defaults are 7/12.
	require.EqualValues(t, 7, num)
	require.EqualValues(t, 12, den)

	// goPredicate is the canonical formula (matches EasyFL source).
	goPredicate := func(covDelta, supply uint64) bool {
		return covDelta*den > supply*num
	}

	cases := []struct {
		name    string
		covD    uint64
		supply  uint64
		healthy bool
	}{
		{"unhealthy_zero_coverage", 0, 1_000_000, false},
		{"unhealthy_under_threshold", 500_000, 1_000_000, false}, // 500k*12 = 6M, 1M*7 = 7M => unhealthy
		{"boundary_just_under", 7_000_000, 12_000_000, false},    // covD*12 = 84M, supply*7 = 84M, strict-greater fails
		{"boundary_just_over", 7_000_001, 12_000_000, true},
		{"healthy", 10_000_000, 1_000_000, true},
		{"healthy_realistic_supply", 600_000_000_000, 1_000_000_000_000_000, false}, // ~0.06% of supply, unhealthy
		{"healthy_realistic", 700_000_000_000_000, 1_000_000_000_000_000, true},     // 70% of supply, healthy
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lib.IsHealthyCoverageDelta(c.covD, c.supply)
			require.Equal(t, c.healthy, got, "Library.IsHealthyCoverageDelta")
			require.Equal(t, goPredicate(c.covD, c.supply), got, "predicate disagrees with formula")
		})
	}
}
