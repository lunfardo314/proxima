package snapshot_restore

// A node without a DB, or with a DB it judges too old, fetches a snapshot from its `sources` at start.
// Nothing later on the startup path checks the snapshot's ledger identity: the node takes it from the
// restored DB. These tests check that one source cannot decide which ledger the node starts on, nor
// get a valid DB replaced by claiming the network is far ahead. Required properties: the snapshot taken
// belongs to the ledger identity of a strict majority of the configured sources, and the network state
// used for the too-old decision is not the maximum any single source reports.
// The sources are local httptest servers.

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// fakeSource serves the endpoints the startup path queries: a snapshot file claimed to be at
// snapshotSlot, sync info with a current slot 1000 later, and the ledger constants (incl. the
// identity hash) of that file.
func fakeSource(t *testing.T, snapshotPath string, snapshotSlot uint32) *httptest.Server {
	return fakeSourceAt(t, snapshotPath, snapshotSlot, snapshotSlot+1000, snapshotSlot+999, false)
}

// fakeSourceAt is fakeSource with explicit sync info. refuseInfo makes the source refuse to offer
// its snapshot, as an honest node does while its newest snapshot is younger than
// SnapshotMinAgeSlots, while still answering sync info and constants.
func fakeSourceAt(t *testing.T, snapshotPath string, snapshotSlot, currentSlot, lrbSlot uint32, refuseInfo bool) *httptest.Server {
	t.Helper()
	data, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	kv, err := multistate.OpenSnapshotFileStream(snapshotPath)
	require.NoError(t, err)
	consts, err := kv.GetLedgerConstants()
	require.NoError(t, err)
	kv.Close()
	constsJSON, err := json.Marshal(consts)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc(api.PathGetSnapshotInfo, func(w http.ResponseWriter, _ *http.Request) {
		if refuseInfo {
			_ = json.NewEncoder(w).Encode(api.Error{Error: "refusing to serve snapshot: ageOK=false"})
			return
		}
		_ = json.NewEncoder(w).Encode(api.SnapshotInfo{Slot: snapshotSlot, FileSize: int64(len(data)), FileName: filepath.Base(snapshotPath)})
	})
	mux.HandleFunc(api.PathGetSyncInfo, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.SyncInfo{Synced: true, CurrentSlot: currentSlot, LrbSlot: lrbSlot})
	})
	mux.HandleFunc(api.PathGetSnapshot, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(snapshotPath)))
		_, _ = w.Write(data)
	})
	mux.HandleFunc(api.PathGetLedgerConstants, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(constsJSON)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func ledgerHashOf(t *testing.T, snapshotPath string) [32]byte {
	t.Helper()
	h, err := snapshotLedgerHash(snapshotPath)
	require.NoError(t, err)
	return h
}

// networkAndForeignGenesis makes the genesis snapshots of two ledgers that differ only in the genesis
// key: the network's, and a foreign one anybody can make without any key of the network.
func networkAndForeignGenesis(t *testing.T) (netSnap, foreignSnap string) {
	genesisTime := uint32(time.Now().Unix())
	ledger.ResetForTesting()
	_, keyA, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	netSnap, _, err = multistate.CreateGenesisSnapshot(keyA, genesisTime, "network", t.TempDir())
	require.NoError(t, err)
	ledger.ResetForTesting()
	_, keyB, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	foreignSnap, _, err = multistate.CreateGenesisSnapshot(keyB, genesisTime, "foreign", t.TempDir())
	require.NoError(t, err)
	require.NotEqual(t, ledgerHashOf(t, netSnap), ledgerHashOf(t, foreignSnap))
	return
}

func setSources(t *testing.T, srv ...*httptest.Server) {
	urls := make([]string, len(srv))
	for i, s := range srv {
		urls[i] = s.URL
	}
	viper.Set("sources", urls)
	t.Cleanup(func() { viper.Set("sources", nil) })
}

// Start without a DB: one of three sources serves a foreign ledger with a newer claimed snapshot.
// The node must take the snapshot of the two others.
func TestSnapshotSourceMajorityIdentity(t *testing.T) {
	netSnap, foreignSnap := networkAndForeignGenesis(t)

	liar := fakeSource(t, foreignSnap, 2000)
	honest1 := fakeSource(t, netSnap, 1000)
	honest2 := fakeSource(t, netSnap, 1000)
	setSources(t, liar, honest1, honest2)

	downloaded := tryDownloadRemoteSnapshot(global.NewDefault(), t.TempDir())
	require.NotEmpty(t, downloaded, "a snapshot is downloaded")
	require.Equal(t, ledgerHashOf(t, netSnap), ledgerHashOf(t, downloaded),
		"the snapshot taken belongs to a foreign ledger: one of three sources decided")
}

// Restart with a valid DB of the network: the too-old check runs at every start, whatever
// snapshot_restore.enable says. A foreign source claiming a network far ahead must not get the DB
// replaced by its ledger.
func TestTooOldRecoveryMajorityIdentity(t *testing.T) {
	netSnap, foreignSnap := networkAndForeignGenesis(t)

	t.Chdir(t.TempDir())
	_, err := RestoreFromSnapshot(netSnap, DefaultRestoreOptions())
	require.NoError(t, err)
	dbSlot, ttl, ok := latestCommittedSlotAndTTLInDB(global.MultiStateDBName)
	require.True(t, ok, "DB readable - otherwise the test checks nothing")

	liarSlot := dbSlot + ttl/2 + 1000 // far enough ahead that the node would consider itself too old
	liar := fakeSource(t, foreignSnap, liarSlot)
	honest1 := fakeSource(t, netSnap, dbSlot+1000)
	honest2 := fakeSource(t, netSnap, dbSlot+1000)
	setSources(t, liar, honest1, honest2)

	f, err := checkStateTooOldDownload(global.NewDefault())
	if err != nil || f == "" {
		return // DB kept or start refused: no foreign ledger
	}
	require.Equal(t, ledgerHashOf(t, netSnap), ledgerHashOf(t, f),
		"the valid DB would be replaced by the snapshot of a foreign ledger")
}

// Honest sources refuse to offer a snapshot younger than SnapshotMinAgeSlots, so for a while the one
// source that always answers is the only one offering. Its foreign ledger must still not be taken:
// the identity vote counts all configured sources, not only those offering a snapshot.
func TestSnapshotSourceHonestRefuseWindow(t *testing.T) {
	netSnap, foreignSnap := networkAndForeignGenesis(t)

	liar := fakeSource(t, foreignSnap, 2000)
	honest1 := fakeSourceAt(t, netSnap, 1000, 2000, 1999, true)
	honest2 := fakeSourceAt(t, netSnap, 1000, 2000, 1999, true)
	setSources(t, liar, honest1, honest2)

	downloaded := tryDownloadRemoteSnapshot(global.NewDefault(), t.TempDir())
	if downloaded == "" {
		return // no download: the node takes no foreign ledger
	}
	require.Equal(t, ledgerHashOf(t, netSnap), ledgerHashOf(t, downloaded),
		"honest sources currently refuse (snapshot too young): the one lying source decides alone")
}

// Restart with a valid, recent DB: one of three sources, on the same ledger, claims the network is
// far ahead. The network state is the median of the sources, so the DB is kept and nothing is
// downloaded; with the maximum, the DB would be replaced by that source's snapshot.
func TestTooOldRecoveryOneSourceAhead(t *testing.T) {
	netSnap, _ := networkAndForeignGenesis(t)

	t.Chdir(t.TempDir())
	_, err := RestoreFromSnapshot(netSnap, DefaultRestoreOptions())
	require.NoError(t, err)
	dbSlot, ttl, ok := latestCommittedSlotAndTTLInDB(global.MultiStateDBName)
	require.True(t, ok, "DB readable - otherwise the test checks nothing")

	st := ttl / 2
	ahead := fakeSource(t, netSnap, dbSlot+st+1000)
	honest1 := fakeSourceAt(t, netSnap, dbSlot, dbSlot+1, dbSlot, false)
	honest2 := fakeSourceAt(t, netSnap, dbSlot, dbSlot+1, dbSlot, false)
	setSources(t, ahead, honest1, honest2)

	BootstrapFromOldState.Store(false)
	f, err := checkStateTooOldDownload(global.NewDefault())
	require.NoError(t, err)
	require.Empty(t, f, "a valid, recent DB is replaced because one source claims the network is far ahead")
	require.False(t, BootstrapFromOldState.Load(), "the node is not in the stalled-network case")
}

// Two sources configured, one of them unreachable: the one left is not a strict majority of the
// configured sources, so no snapshot is downloaded (the node falls back to its local snapshot directory).
func TestSnapshotSourceNoMajorityRefused(t *testing.T) {
	netSnap, _ := networkAndForeignGenesis(t)

	honest := fakeSource(t, netSnap, 1000)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	setSources(t, honest, down)

	require.Empty(t, tryDownloadRemoteSnapshot(global.NewDefault(), t.TempDir()),
		"one of two configured sources decided the ledger identity")
}

// One source configured: it alone would decide which ledger the node starts on, so no snapshot is
// downloaded; the operator configures more sources or restores manually.
func TestSnapshotSourceSingleRefused(t *testing.T) {
	netSnap, _ := networkAndForeignGenesis(t)

	setSources(t, fakeSource(t, netSnap, 1000))

	require.Empty(t, tryDownloadRemoteSnapshot(global.NewDefault(), t.TempDir()),
		"a single configured source decided the ledger identity")
}
