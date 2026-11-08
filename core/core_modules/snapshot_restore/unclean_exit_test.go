package snapshot_restore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"
)

// TestUncleanExitDBNotDeleted checks that a DB left by an unclean exit (a memtable log that was
// never flushed) is opened by the startup checks and kept, not taken for a corrupted one and
// deleted. Badger refuses a read-only open of such a DB with ErrTruncateNeeded; a read-write open
// replays the log.
func TestUncleanExitDBNotDeleted(t *testing.T) {
	srcDir := filepath.Join(t.TempDir(), "src")
	db, err := badger.Open(badger.DefaultOptions(srcDir).WithLogger(nil))
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("key"), []byte("value"))
	}))

	// copy the files while the DB is still open: the state a killed process leaves on disk
	dbPath := filepath.Join(t.TempDir(), "unclean")
	require.NoError(t, os.MkdirAll(dbPath, 0o755))
	entries, err := os.ReadDir(srcDir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NoError(t, CopyFile(filepath.Join(srcDir, e.Name()), filepath.Join(dbPath, e.Name())))
	}

	// the plain read-only open fails: this is the error that used to get the DB deleted
	_, err = badger.Open(badger.DefaultOptions(dbPath).WithReadOnly(true).WithLogger(nil))
	require.ErrorContains(t, err, badger.ErrTruncateNeeded.Error())

	deleted, err := CheckAndDeleteCorruptedDB(dbPath, nil)
	require.NoError(t, err)
	require.False(t, deleted)

	// the DB is still there, with the write that was only in the memtable log
	recovered, err := openDBReadOnly(dbPath)
	require.NoError(t, err)
	defer recovered.Close()
	require.NoError(t, recovered.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("key"))
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			require.Equal(t, []byte("value"), v)
			return nil
		})
	}))
}
