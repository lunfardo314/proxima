package client

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDownloadSnapshotName checks that the file name a server sends with a snapshot is used only when it
// is a plain snapshot file name. A name with a directory part, a hidden name or another extension is
// ignored and the default name is used inside the destination directory, so a server can never place
// the file anywhere else.
func TestDownloadSnapshotName(t *testing.T) {
	for name, want := range map[string]string{
		"100-5-ab.snapshot":    "100-5-ab.snapshot",
		"../escape.snapshot":   "downloaded.snapshot",
		"sub/inner.snapshot":   "downloaded.snapshot",
		"/etc/passwd.snapshot": "downloaded.snapshot",
		".hidden.snapshot":     "downloaded.snapshot",
		"not-a-snapshot.txt":   "downloaded.snapshot",
		"":                     "downloaded.snapshot",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if name != "" {
				w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
			}
			_, _ = w.Write([]byte("snapshot bytes"))
		}))
		dir := t.TempDir()
		got, err := NewWithGoogleDNS(srv.URL).DownloadSnapshot(dir)
		srv.Close()
		require.NoError(t, err, name)
		require.Equal(t, filepath.Join(dir, want), got, name)
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, entries, 1, name)
	}
}
