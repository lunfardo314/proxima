package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

// snapshotServeMinAgeSlots is the minimum age (in slots) of the latest snapshot
// before it is served over the API. A snapshot younger than this is withheld
// unless it is within the same distance from genesis (a young network has no
// older snapshot to offer) or snapshot.always_serve overrides the gate.
const snapshotServeMinAgeSlots = 64

// decideServeSnapshot locates the latest snapshot and decides whether it may be
// served. Unless snapshot.always_serve is set, the snapshot is served only if ALL:
//   - the node is synced,
//   - the snapshot is >= snapshotServeMinAgeSlots old OR within that many slots of
//     genesis,
//   - the snapshot branch is in the past of the node's latest reliable branch (LRB).
//
// The branch ID and slot are read authoritatively from the snapshot file header,
// not parsed from the filename. On refusal, diag explains which checks failed.
func (srv *server) decideServeSnapshot() (fpath string, slot uint32, servable bool, diag string, err error) {
	fpath, err = srv.GetSnapshotFilePath()
	if err != nil {
		return "", 0, false, "", err
	}
	stream, err := multistate.OpenSnapshotFileStream(fpath)
	if err != nil {
		return "", 0, false, "", fmt.Errorf("cannot read snapshot '%s': %w", filepath.Base(fpath), err)
	}
	branchID := stream.BranchID
	stream.Close()
	slot = branchID.Slot()

	if viper.GetBool("snapshot.always_serve") {
		return fpath, slot, true, "", nil
	}

	currentSlot := uint32(ledger.TimeNow().Slot)
	synced := srv.GetSyncInfo().Synced
	nearGenesis := slot < snapshotServeMinAgeSlots
	oldEnough := currentSlot >= slot+snapshotServeMinAgeSlots
	ageOK := nearGenesis || oldEnough

	// depth from the LRB down to the snapshot branch is at most the slot gap;
	// add a margin so a slightly stale LRB still resolves the ancestor.
	maxDepth := int(snapshotServeMinAgeSlots)
	if currentSlot > slot {
		maxDepth += int(currentSlot - slot)
	}
	_, foundAtDepth := srv.CheckTransactionInLRB(branchID, maxDepth)
	inLRBPast := foundAtDepth >= 0

	servable = synced && ageOK && inLRBPast
	if !servable {
		diag = fmt.Sprintf("refusing to serve snapshot %s (slot %d): synced=%v, ageOK=%v (currentSlot=%d, age=%d, required>=%d slots or <%d from genesis), inLRBPast=%v (foundAtDepth=%d). Set snapshot.always_serve=true to override",
			filepath.Base(fpath), slot, synced, ageOK, currentSlot, int64(currentSlot)-int64(slot), snapshotServeMinAgeSlots, snapshotServeMinAgeSlots, inLRBPast, foundAtDepth)
	}
	return fpath, slot, servable, diag, nil
}

// getSnapshotInfo returns metadata about the latest available snapshot (slot, size, filename).
// It applies the same serve gate as the download, so a source-selecting client (e.g. snapshot_restore)
// skips nodes that would refuse to serve.
func (srv *server) getSnapshotInfo(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	if !viper.GetBool("snapshot.enable_api") {
		api.WriteErr(w, "snapshot API is disabled")
		return
	}

	fpath, slot, servable, diag, err := srv.decideServeSnapshot()
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	if !servable {
		api.WriteErr(w, diag)
		return
	}

	fi, err := os.Stat(fpath)
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	resp := api.SnapshotInfo{
		Slot:     slot,
		FileSize: fi.Size(),
		FileName: filepath.Base(fpath),
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

// writeSnapshotDownloadErr reports a download failure with a non-200 status, so a
// client (which keys off the HTTP status) treats it as an error rather than saving
// the error body as a snapshot file.
func writeSnapshotDownloadErr(w http.ResponseWriter, msg string) {
	api.SetHeader(w)
	w.WriteHeader(http.StatusServiceUnavailable)
	api.WriteErr(w, msg)
}

func (srv *server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	if !viper.GetBool("snapshot.enable_api") {
		writeSnapshotDownloadErr(w, "snapshot download API is disabled")
		return
	}

	fpath, _, servable, diag, err := srv.decideServeSnapshot()
	if err != nil {
		writeSnapshotDownloadErr(w, err.Error())
		return
	}
	if !servable {
		writeSnapshotDownloadErr(w, diag)
		return
	}

	// Open the file before serving so the fd is pinned.
	// This prevents races with snapshot purge: once open, the inode
	// stays valid even if the directory entry is removed.
	f, err := os.Open(fpath)
	if err != nil {
		writeSnapshotDownloadErr(w, err.Error())
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		writeSnapshotDownloadErr(w, err.Error())
		return
	}

	// Extend the write deadline for this response since snapshot files
	// can be large and the default server WriteTimeout (10s) is too short.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))
	}

	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(fpath)+`"`)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.ServeContent(w, r, filepath.Base(fpath), fi.ModTime(), f)
}
