package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// snapshotFileName names a snapshot file the way SaveSnapshot does, after a branch ID of the slot
func snapshotFileName(slot uint32) string {
	branchID := base.RandomTransactionID(true, 1, base.T(slot, 0))
	return branchID.AsFileName() + ".snapshot"
}

// TestPreUpgradeSnapshotRetention checks that snapshots of the last hour before an upgrade
// slot survive the periodic purge whatever keep_latest is, and do not count toward it.
func TestPreUpgradeSnapshotRetention(t *testing.T) {
	ledger.InitWithTestingLedgerData()

	const upgradeSlot = 29000
	saved := ledger.Upgrades
	ledger.Upgrades = []*ledger.UpgradeDefinition{{Slot: upgradeSlot}}
	defer func() { ledger.Upgrades = saved }()

	keepSlots := uint32(preUpgradeKeepPeriod / ledger.L(0).SlotDuration())

	// the window is [upgradeSlot - keepSlots, upgradeSlot)
	require.True(t, IsPreUpgradeSnapshot(snapshotFileName(upgradeSlot-1)))
	require.True(t, IsPreUpgradeSnapshot(snapshotFileName(upgradeSlot-keepSlots)))
	require.False(t, IsPreUpgradeSnapshot(snapshotFileName(upgradeSlot-keepSlots-1)))
	require.False(t, IsPreUpgradeSnapshot(snapshotFileName(upgradeSlot)))
	require.False(t, IsPreUpgradeSnapshot("not-a-branch.snapshot"))

	dir := t.TempDir()
	slots := []uint32{upgradeSlot - 2000, upgradeSlot - 1000, upgradeSlot - keepSlots + 5, upgradeSlot - 10, upgradeSlot + 100, upgradeSlot + 200}
	names := make([]string, len(slots))
	now := time.Now()
	for i, slot := range slots {
		names[i] = snapshotFileName(slot)
		fpath := filepath.Join(dir, names[i])
		require.NoError(t, os.WriteFile(fpath, []byte("x"), 0o644))
		// modification times in slot order: the purge keeps the newest by time
		mt := now.Add(time.Duration(i-len(slots)) * time.Minute)
		require.NoError(t, os.Chtimes(fpath, mt, mt))
	}

	// keep_latest 1: the newest one stays, and the two of the last hour before the upgrade
	require.NoError(t, util.PurgeFilesInDirectory(dir, "*.snapshot", 1, IsPreUpgradeSnapshot))
	for i, name := range names {
		_, err := os.Stat(filepath.Join(dir, name))
		kept := i == 2 || i == 3 || i == 5
		require.Equal(t, kept, err == nil, "slot %d", slots[i])
	}

	// keep_latest 0 (the restore-time cleanup): only the pre-upgrade ones stay
	require.NoError(t, util.PurgeFilesInDirectory(dir, "*.snapshot", 0, IsPreUpgradeSnapshot))
	for i, name := range names {
		_, err := os.Stat(filepath.Join(dir, name))
		require.Equal(t, i == 2 || i == 3, err == nil, "slot %d", slots[i])
	}
}
