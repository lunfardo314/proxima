package global

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSaveCrashLog verifies that saveCrashLog copies the current log file to a
// crash-<log basename>.<unix> file next to it, preserving its content (the crash reason). This is
// the shared path used by both GracefulShutdown and the zap fatal hook.
func TestSaveCrashLog(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "proxima.log")
	content := []byte("line1\nGRACEFUL SHUTDOWN: boom\n")
	require.NoError(t, os.WriteFile(logFile, content, 0644))

	g := NewDefault()
	g.logFilename = logFile
	g.saveCrashLog()

	// crash log carries the node's log basename so nodes sharing a directory stay distinct
	matches, err := filepath.Glob(filepath.Join(dir, "crash-proxima.log.*"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "exactly one crash log expected")

	got, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	require.Equal(t, content, got)
}

// TestSaveCrashLogNoFile verifies saveCrashLog is a no-op when logging to stdout only.
func TestSaveCrashLogNoFile(t *testing.T) {
	g := NewDefault()
	require.Empty(t, g.logFilename)
	g.saveCrashLog() // must not panic or create anything
}
