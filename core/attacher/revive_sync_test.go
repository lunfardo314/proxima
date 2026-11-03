package attacher

import (
	"testing"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// While forward sync drives a target, reviving milestones newer than that target only
// re-creates live traffic that cannot solidify yet. The same-slot and older milestones
// stay eligible because they can belong to the target's past cone.
func TestSyncModeShedsRevivalAheadOfTarget(t *testing.T) {
	target := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 100, Tick: 0})
	require.True(t, global.AddSyncTarget(target))
	defer global.RemoveSyncTarget(target)

	ahead := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 101, Tick: 3})
	atTarget := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 100, Tick: 5})
	behind := base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 99, Tick: 7})

	require.True(t, syncModeShedsRevival(ahead))
	require.False(t, syncModeShedsRevival(atTarget))
	require.False(t, syncModeShedsRevival(behind))
}
