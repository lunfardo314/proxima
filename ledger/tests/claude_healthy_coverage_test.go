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
// definition: covDelta * denominator > supply * numerator.
//
// The default test ledger uses a relaxed (0, 1) fraction so synthetic
// short-past-cone test branches can pass the on-chain healthiness check
// (see GetTestingLedgerParams). This test validates the predicate against
// whatever (num, den) the current library carries — so it works for both
// the relaxed test mode (num=0) and the production 7/12 fraction.
func TestHealthyCoverageDelta(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	num := lib.HealthyCoverageNumerator
	den := lib.HealthyCoverageDenominator

	t.Logf("library healthy-coverage fraction: %d/%d", num, den)
	require.True(t, den > 0, "denominator must be > 0")

	// goPredicate is the canonical formula (matches EasyFL source).
	goPredicate := func(covDelta, supply uint64) bool {
		return covDelta*den > supply*num
	}

	// Boundary cases scaled by the current fraction so the test exercises
	// both relaxed (num=0) and strict (num=7) modes meaningfully.
	cases := []struct {
		name   string
		covD   uint64
		supply uint64
	}{
		{"zero_coverage_zero_supply", 0, 0},
		{"zero_coverage_positive_supply", 0, 1_000_000},
		{"positive_coverage_zero_supply", 1, 0},
		{"low_coverage", 500_000, 1_000_000},
		{"boundary_under", num * 1_000_000, den * 1_000_000},     // covD*den == supply*num — strict-> fails
		{"boundary_over", num*1_000_000 + 1, den * 1_000_000},    // just above the threshold
		{"high_coverage", 10_000_000, 1_000_000},
		{"big_supply_low_cov", 600_000_000_000, 1_000_000_000_000_000},
		{"big_supply_high_cov", 700_000_000_000_000, 1_000_000_000_000_000},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expected := goPredicate(c.covD, c.supply)
			got := lib.IsHealthyCoverageDelta(c.covD, c.supply)
			require.Equal(t, expected, got, "EasyFL precompiled call must match Go cross-multiplication formula")
		})
	}
}
