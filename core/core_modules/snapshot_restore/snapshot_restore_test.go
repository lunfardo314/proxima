package snapshot_restore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStateFileManager_CreateNew(t *testing.T) {
	tmpDir := t.TempDir()
	stateFilePath := filepath.Join(tmpDir, ".snapshot_restore_test.json")

	mgr, err := NewStateFileManager(stateFilePath)
	require.NoError(t, err)
	require.NotNil(t, mgr)

	// Verify initial state
	require.False(t, mgr.IsCleanupInProgress())
	require.Equal(t, uint32(0), mgr.GetNextCleanupSlot())
	require.Nil(t, mgr.GetCleanupStartedAt())
	require.Empty(t, mgr.GetSnapshotFile())

	// Verify file was created
	_, err = os.Stat(stateFilePath)
	require.NoError(t, err)
}

func TestStateFileManager_SetNextCleanupSlot(t *testing.T) {
	tmpDir := t.TempDir()
	stateFilePath := filepath.Join(tmpDir, ".snapshot_restore_test.json")

	mgr, err := NewStateFileManager(stateFilePath)
	require.NoError(t, err)

	// Set next cleanup slot
	err = mgr.SetNextCleanupSlot(12345)
	require.NoError(t, err)
	require.Equal(t, uint32(12345), mgr.GetNextCleanupSlot())

	// Reload and verify persistence
	mgr2, err := NewStateFileManager(stateFilePath)
	require.NoError(t, err)
	require.Equal(t, uint32(12345), mgr2.GetNextCleanupSlot())
}

func TestStateFileManager_StartAndCompleteCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	stateFilePath := filepath.Join(tmpDir, ".snapshot_restore_test.json")

	mgr, err := NewStateFileManager(stateFilePath)
	require.NoError(t, err)

	// Start cleanup
	snapshotFile := "/path/to/snapshot.snapshot"
	err = mgr.StartCleanup(snapshotFile)
	require.NoError(t, err)

	require.True(t, mgr.IsCleanupInProgress())
	require.Equal(t, snapshotFile, mgr.GetSnapshotFile())
	require.NotNil(t, mgr.GetCleanupStartedAt())

	// Complete cleanup
	nextSlot := uint32(99999)
	err = mgr.CompleteCleanup(nextSlot)
	require.NoError(t, err)

	require.False(t, mgr.IsCleanupInProgress())
	require.Empty(t, mgr.GetSnapshotFile())
	require.Nil(t, mgr.GetCleanupStartedAt())
	require.Equal(t, nextSlot, mgr.GetNextCleanupSlot())

	state := mgr.GetState()
	require.NotNil(t, state.LastCleanupCompleted)
}

func TestStateFileManager_IsCleanupTTLExceeded(t *testing.T) {
	tmpDir := t.TempDir()
	stateFilePath := filepath.Join(tmpDir, ".snapshot_restore_test.json")

	mgr, err := NewStateFileManager(stateFilePath)
	require.NoError(t, err)

	// Not in progress - should return false
	require.False(t, mgr.IsCleanupTTLExceeded(1*time.Minute))

	// Start cleanup
	err = mgr.StartCleanup("/path/to/snapshot.snapshot")
	require.NoError(t, err)

	// Just started - TTL not exceeded
	require.False(t, mgr.IsCleanupTTLExceeded(1*time.Minute))

	// With 0 TTL, should be exceeded
	require.True(t, mgr.IsCleanupTTLExceeded(0))
}

func TestStateFileManager_ResetCleanupState(t *testing.T) {
	tmpDir := t.TempDir()
	stateFilePath := filepath.Join(tmpDir, ".snapshot_restore_test.json")

	mgr, err := NewStateFileManager(stateFilePath)
	require.NoError(t, err)

	// Start cleanup
	err = mgr.StartCleanup("/path/to/snapshot.snapshot")
	require.NoError(t, err)
	require.True(t, mgr.IsCleanupInProgress())

	// Reset
	err = mgr.ResetCleanupState()
	require.NoError(t, err)

	require.False(t, mgr.IsCleanupInProgress())
	require.Empty(t, mgr.GetSnapshotFile())
	require.Nil(t, mgr.GetCleanupStartedAt())
}

func TestCheckPermissions(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test file to simulate snapshot
	snapshotFile := filepath.Join(tmpDir, "test.snapshot")
	err := os.WriteFile(snapshotFile, []byte("test"), 0644)
	require.NoError(t, err)

	// Should succeed with valid paths
	err = CheckPermissions(tmpDir, snapshotFile)
	require.NoError(t, err)

	// Should fail with non-existent snapshot
	err = CheckPermissions(tmpDir, "/nonexistent/path/snapshot.snapshot")
	require.Error(t, err)
}

func TestFindLatestSnapshot(t *testing.T) {
	tmpDir := t.TempDir()

	// No snapshots - should fail
	_, err := FindLatestSnapshot(tmpDir)
	require.Error(t, err)

	// Create some test snapshot files with different modification times
	file1 := filepath.Join(tmpDir, "snapshot1.snapshot")
	file2 := filepath.Join(tmpDir, "snapshot2.snapshot")
	file3 := filepath.Join(tmpDir, "notasnapshot.txt")

	err = os.WriteFile(file1, []byte("snapshot1"), 0644)
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond) // Ensure different mod times

	err = os.WriteFile(file2, []byte("snapshot2"), 0644)
	require.NoError(t, err)

	err = os.WriteFile(file3, []byte("not a snapshot"), 0644)
	require.NoError(t, err)

	// Should find the latest snapshot (file2)
	latest, err := FindLatestSnapshot(tmpDir)
	require.NoError(t, err)
	require.Equal(t, file2, latest)
}

func TestDeleteDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "testdb")

	// Create a directory to simulate a database
	err := os.MkdirAll(dbPath, 0755)
	require.NoError(t, err)

	// Create some files in it
	err = os.WriteFile(filepath.Join(dbPath, "test.db"), []byte("data"), 0644)
	require.NoError(t, err)

	// Delete should succeed
	err = DeleteDatabase(dbPath)
	require.NoError(t, err)

	// Directory should be gone
	_, err = os.Stat(dbPath)
	require.True(t, os.IsNotExist(err))

	// Delete non-existent should succeed (idempotent)
	err = DeleteDatabase(dbPath)
	require.NoError(t, err)
}

func TestStateFileManager_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	stateFilePath := filepath.Join(tmpDir, ".snapshot_restore_test.json")

	// Create and set up initial state
	mgr1, err := NewStateFileManager(stateFilePath)
	require.NoError(t, err)

	err = mgr1.SetNextCleanupSlot(54321)
	require.NoError(t, err)

	err = mgr1.StartCleanup("/path/to/snapshot.snapshot")
	require.NoError(t, err)

	// Load from same file - state should persist
	mgr2, err := NewStateFileManager(stateFilePath)
	require.NoError(t, err)

	require.Equal(t, uint32(54321), mgr2.GetNextCleanupSlot())
	require.True(t, mgr2.IsCleanupInProgress())
	require.Equal(t, "/path/to/snapshot.snapshot", mgr2.GetSnapshotFile())
}
