package delegationpool

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// Snapshot adds to an epoch's load every other sequencer's frozen delegation whose
// safe-revocation window overlaps the window the end of that epoch opens on this
// sequencer's grid, so a freeze avoids unfreezing together with capital elsewhere.
// Windows that miss every reachable epoch's window add nothing, and the per-epoch
// count stays own-only.
func TestSnapshotForeignLoad(t *testing.T) {
	ledger.InitWithTestingLedgerData()
	target := base.RandomChainID()
	const currentSlot = 100_000
	lib := ledger.L(currentSlot)
	epochSlots, safeSlots := lib.DelegationEpochSlots, lib.SafeRevocationSlots
	txEpoch := lib.EpochFromSlotDirect(target, currentSlot, epochSlots)
	// window start opened by the end of epoch e on the target's grid
	start := func(e uint32) uint32 { return lib.LastSlotInEpochDirect(target, e, epochSlots) + 1 }

	p := &DelegationPool{target: target, entries: map[base.ChainID]*delegationEntry{}, foreign: []foreignWindow{
		{start: start(txEpoch + 5), amount: 1000},              // exactly on epoch 5's window
		{start: start(txEpoch+7) + safeSlots - 1, amount: 200}, // last slot overlaps epoch 7's window
		{start: start(txEpoch+9) - safeSlots + 1, amount: 30},  // ends in the first slot of epoch 9's window
		{start: start(txEpoch+11) + safeSlots, amount: 4},      // starts right after epoch 11's window: no overlap
		{start: start(txEpoch + 3*epochSlots), amount: 7},      // far beyond the reach
	}}
	_, load, count := p.Snapshot(currentSlot, 1000)
	require.EqualValues(t, map[uint32]uint64{txEpoch + 5: 1000, txEpoch + 7: 200, txEpoch + 9: 30}, load)
	require.Empty(t, count)
}
