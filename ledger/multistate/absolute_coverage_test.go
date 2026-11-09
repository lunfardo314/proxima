package multistate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Absolute coverage (kb/dag_semantics.md, D8) with the numbers of the 2026-11-09 rollback: the
// healthy lineage's branch of slot 187949 (ledger coverage about 142T) against branches of slot
// 187951 built directly on branch 187557 (delta 69.42T against the state of 187557).
func TestAbsoluteCoverageRollback(t *testing.T) {
	const T = uint64(1_000_000_000_000)
	healthy187949 := 142 * T
	stored187557 := 142 * T
	rollbackDelta := uint64(69_422_004_132_735)

	rollback := AbsoluteCoverage(stored187557, 187557, 187951, rollbackDelta)
	require.EqualValues(t, 0, rollback)

	// measured at 187951 the healthy lineage keeps 142T >> 2
	require.Equal(t, healthy187949>>2, CoverageAtSlot(healthy187949, 187949, 187951))
	require.Greater(t, CoverageAtSlot(healthy187949, 187949, 187951), CoverageAtSlot(rollback, 187951, 187951))

	// halving the inherited part only, the rollback would have won
	inheritedOnly := stored187557>>(187951-187557) + rollbackDelta
	require.Greater(t, inheritedOnly, CoverageAtSlot(healthy187949, 187949, 187951))
}

// A normal branch, one slot after its predecessor, has its stored ledger coverage: the delta in
// full plus half the predecessor. A gap of one slot halves both parts once more, for every
// candidate alike.
func TestAbsoluteCoverageGaps(t *testing.T) {
	require.EqualValues(t, 50+70, AbsoluteCoverage(100, 9, 10, 70))
	require.EqualValues(t, 25+35, AbsoluteCoverage(100, 9, 11, 70))
	require.EqualValues(t, 6+8, AbsoluteCoverage(100, 9, 13, 70))
}

// A descendant always beats its ancestor measured at the descendant's slot, even with a tiny delta
// and after a long gap: it inherits the ancestor's halved coverage and adds to it.
func TestAbsoluteCoverageDescendantBeatsAncestor(t *testing.T) {
	ancestor := uint64(142_000)
	for gap := uint32(1); gap <= 20; gap++ {
		descendant := AbsoluteCoverage(ancestor, 100, 100+gap, 1<<gap)
		require.Greater(t, descendant, CoverageAtSlot(ancestor, 100, 100+gap), "gap %d", gap)
	}
}
