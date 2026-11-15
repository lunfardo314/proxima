package snapshot_restore

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/stretchr/testify/require"
)

// TestRestoreRefusesCorruptSnapshot checks restore from a snapshot file that may come from another node.
// A valid genesis snapshot restores and clears the restore-in-progress marker. A truncated copy and a
// copy with its last byte changed fail with an error instead of a panic, and leave the marker set, so
// the next start discards the database instead of opening a state that does not match its root.
func TestRestoreRefusesCorruptSnapshot(t *testing.T) {
	ledger.ResetForTesting()
	_, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	fpath, _, err := multistate.CreateGenesisSnapshot(privateKey, uint32(time.Now().Unix()), "restore corrupt test", t.TempDir())
	require.NoError(t, err)
	good, err := os.ReadFile(fpath)
	require.NoError(t, err)

	// restore runs in the working directory, where it creates the database
	restoreFrom := func(data []byte) (markerSet bool, err error) {
		t.Chdir(t.TempDir())
		snap := filepath.Join(t.TempDir(), filepath.Base(fpath))
		require.NoError(t, os.WriteFile(snap, data, 0o644))
		_, err = RestoreFromSnapshot(snap, DefaultRestoreOptions())
		db := badger_adaptor.MustCreateOrOpenBadgerDB(global.MultiStateDBName)
		store := badger_adaptor.New(db)
		markerSet = multistate.IsRestoreInProgress(store)
		_ = store.Close()
		return
	}

	markerSet, err := restoreFrom(good)
	require.NoError(t, err)
	require.False(t, markerSet)

	markerSet, err = restoreFrom(good[:len(good)-7])
	require.Error(t, err)
	require.True(t, markerSet, "truncated snapshot")

	changed := append([]byte{}, good...)
	changed[len(changed)-1] ^= 0xff
	markerSet, err = restoreFrom(changed)
	require.Error(t, err)
	require.True(t, markerSet, "changed snapshot")
}
