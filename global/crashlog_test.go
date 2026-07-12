package global

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
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

// TestGracefulShutdownIdempotent verifies that many concurrent GracefulShutdown calls — the
// pattern seen when a whole batch of attachers is force-detached at once during long forward
// sync — log the reason and save a crash log exactly once, instead of flooding the log with
// repeating shutdown lines and writing a crash log per caller.
func TestGracefulShutdownIdempotent(t *testing.T) {
	dir := t.TempDir()
	logOut := filepath.Join(dir, "out.log")
	// route the logger to a file (plus the default stdout) so we can count the emitted lines
	g := _new(zapcore.DebugLevel, []string{logOut})
	g.logFilename = filepath.Join(dir, "proxima.log")
	require.NoError(t, os.WriteFile(g.logFilename, []byte("history\n"), 0644))

	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g.GracefulShutdown(fmt.Sprintf("reason-%d", i))
		}(i)
	}
	wg.Wait()
	_ = g.SugaredLogger.Sync() // flush the file output (Sync may error on stdout; ignore)

	matches, err := filepath.Glob(filepath.Join(dir, "crash-proxima.log.*"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "exactly one crash log file expected")

	out, err := os.ReadFile(logOut)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(out), "GRACEFUL SHUTDOWN"), "reason logged exactly once")
	require.Equal(t, 1, strings.Count(string(out), "crash log saved as"), "crash log saved exactly once")
}
