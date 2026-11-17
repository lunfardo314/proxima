package node

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// TestSupplyGrowthAllowance checks the ceiling behind the [SUPPLY ALERT] warning: the honest
// growth of a slot (about 100 PROX: chain inflation, one mining reward and the branch bonus)
// stays below it, and the smallest of the November 2026 mints, 1.6M PROX, is far above it,
// for a one-slot sample and for a sample after a long gap.
func TestSupplyGrowthAllowance(t *testing.T) {
	ledger.InitWithTestingLedgerData(ledger.WithFoldedUpgrades(6))
	t.Cleanup(ledger.ResetForTesting)

	supply := uint64(91 * base.MPROX)
	oneSlot := supplyGrowthAllowance(supply, 255000, 255001)
	require.Greater(t, oneSlot, uint64(1_000*base.PROX))
	require.Less(t, oneSlot, uint64(1_600_000*base.PROX))

	gap := supplyGrowthAllowance(supply, 255000, 255100)
	require.Greater(t, gap, uint64(100*110*base.PROX))
	require.Less(t, gap, uint64(1_600_000*base.PROX))
}
