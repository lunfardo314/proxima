package server

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/spf13/viper"
)

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
