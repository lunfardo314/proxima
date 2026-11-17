package transaction

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
)

// TestMineAmountAtSlotMatchesLedger keeps the Go copy of the mining reward schedule, used by
// the issuance bound, equal to _mineAmountAtSlot of the ledger library, before, at and after
// the ramp start, so the bound never falls below what the mine lock lets a transit mint.
func TestMineAmountAtSlotMatchesLedger(t *testing.T) {
	ledger.InitWithTestingLedgerData(ledger.WithFoldedUpgrades(6))
	t.Cleanup(ledger.ResetForTesting)
	lib := ledger.L(0)

	ramp := lib.MineRampStartSlot
	for _, slot := range []uint32{0, 1, ramp - 1, ramp, ramp + 1, ramp + 1000, ramp + 1_000_000} {
		res, err := lib.EvalFromSource(nil, fmt.Sprintf("_mineAmountAtSlot(u64/%d)", slot))
		require.NoError(t, err)
		require.Len(t, res, 8)
		require.Equal(t, binary.BigEndian.Uint64(res), lib.MineAmountAtSlot(slot), "slot %d", slot)
	}
}

// TestAddCapped checks the saturating sum that keeps the bound from wrapping around on huge
// frozen coverage values.
func TestAddCapped(t *testing.T) {
	require.EqualValues(t, 5, addCapped(2, 3))
	require.EqualValues(t, ^uint64(0), addCapped(^uint64(0)-1, 2))
}
