package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The first start of a release keeps the newest snapshot under a name the purge never matches,
// once per release: a safety snapshot from before the release, whatever keep_latest is.
func TestKeepPreReleaseSnapshot(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(name), 0o644))
		ts := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(p, ts, ts))
	}
	// nothing to keep yet
	kept, err := keepPreReleaseSnapshot(dir, "v1.6.0")
	require.NoError(t, err)
	require.Empty(t, kept)

	write("old.snapshot", 2*time.Hour)
	write("new.snapshot", time.Hour)
	write("other.txt", 0)

	kept, err = keepPreReleaseSnapshot(dir, "v1.6.0")
	require.NoError(t, err)
	require.Equal(t, "new.snapshot.pre-v1.6.0", kept, "the newest snapshot")
	b, err := os.ReadFile(filepath.Join(dir, kept))
	require.NoError(t, err)
	require.Equal(t, "new.snapshot", string(b))

	// once per release: a restart of the same release keeps nothing more
	write("newer.snapshot", 0)
	kept, err = keepPreReleaseSnapshot(dir, "v1.6.0")
	require.NoError(t, err)
	require.Empty(t, kept)

	// the next release keeps its own
	kept, err = keepPreReleaseSnapshot(dir, "v1.6.1")
	require.NoError(t, err)
	require.Equal(t, "newer.snapshot.pre-v1.6.1", kept)

	// the purge, which matches *.snapshot, never touches them
	matches, err := filepath.Glob(filepath.Join(dir, "*.snapshot"))
	require.NoError(t, err)
	for _, m := range matches {
		require.NotContains(t, m, ".pre-")
	}
}
