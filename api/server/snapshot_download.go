package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

// getSnapshotInfo returns metadata about the latest available snapshot (slot, size, filename).
func (srv *server) getSnapshotInfo(w http.ResponseWriter, r *http.Request) {
	api.SetHeader(w)

	if !viper.GetBool("snapshot.enable_api") {
		api.WriteErr(w, "snapshot API is disabled")
		return
	}

	fpath, err := srv.GetSnapshotFilePath()
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	fi, err := os.Stat(fpath)
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	// parse slot from filename: first number before '_'
	name := filepath.Base(fpath)
	var slot uint32
	if parts := strings.SplitN(name, "_", 2); len(parts) >= 1 {
		if v, err := strconv.ParseUint(parts[0], 10, 32); err == nil {
			slot = uint32(v)
		}
	}

	resp := api.SnapshotInfo{
		Slot:     slot,
		FileSize: fi.Size(),
		FileName: name,
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, err = w.Write(respBin)
	util.AssertNoError(err)
}

func (srv *server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	if !viper.GetBool("snapshot.enable_api") {
		api.SetHeader(w)
		api.WriteErr(w, "snapshot download API is disabled")
		return
	}

	fpath, err := srv.GetSnapshotFilePath()
	if err != nil {
		api.SetHeader(w)
		api.WriteErr(w, err.Error())
		return
	}

	// Open the file before serving so the fd is pinned.
	// This prevents races with snapshot purge: once open, the inode
	// stays valid even if the directory entry is removed.
	f, err := os.Open(fpath)
	if err != nil {
		api.SetHeader(w)
		api.WriteErr(w, err.Error())
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		api.SetHeader(w)
		api.WriteErr(w, err.Error())
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
