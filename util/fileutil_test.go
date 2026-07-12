package util

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPurgePreservesCrashLogs verifies that files with the "crash" prefix are never removed by
// the log rotation purge, even when they match the purge glob and exceed keepLatest.
func TestPurgePreservesCrashLogs(t *testing.T) {
	dir := t.TempDir()

	// rotated log files that DO match the glob and should be pruned down to keepLatest
	rotated := []string{"proxima.log.1", "proxima.log.2", "proxima.log.3"}
	// crash logs that also match the glob prefix but must survive regardless
	crash := []string{"crash-100.log", "crash-200.log"}

	for _, name := range append(append([]string{}, rotated...), crash...) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644))
	}

	require.NoError(t, PurgeFilesInDirectory(dir, "proxima.log*", 1))

	// all crash logs preserved
	for _, name := range crash {
		_, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err, "crash log %s must be preserved", name)
	}
	// rotated logs purged down to keepLatest (1 kept)
	var remainingRotated int
	for _, name := range rotated {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			remainingRotated++
		}
	}
	require.Equal(t, 1, remainingRotated, "rotated logs should be purged down to keepLatest")
}

// TestCopyFile verifies CopyFile duplicates content into a new destination file.
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.log")
	dst := filepath.Join(dir, "crash-1.log")
	content := []byte("hello crash log")
	require.NoError(t, os.WriteFile(src, content, 0644))

	require.NoError(t, CopyFile(src, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, content, got)
}
