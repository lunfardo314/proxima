package multistate

import (
	"crypto/ed25519"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
)

// TestSnapshotFileStreamCloseReleases opens a snapshot stream and closes it without reading
// the trie data, as the snapshot info and download API does on every request. Close must
// release the iterator goroutine, blocked in a send on the unbuffered channel, and the file.
func TestSnapshotFileStreamCloseReleases(t *testing.T) {
	ledger.ResetForTesting()

	_, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	// a genesis snapshot has trie records after the libraries, so the iterator blocks on them
	fpath, _, err := CreateGenesisSnapshot(privateKey, uint32(time.Now().Unix()), "stream close test", t.TempDir())
	require.NoError(t, err)

	const rounds = 50
	baseGoroutines := runtime.NumGoroutine()
	baseFDs := openFDs()

	for i := 0; i < rounds; i++ {
		stream, err := OpenSnapshotFileStream(fpath)
		require.NoError(t, err)
		stream.Close()
	}

	// the goroutines exit after Close returns, so allow them a moment to finish
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseGoroutines+2
	}, 2*time.Second, 10*time.Millisecond, "iterator goroutines leaked: %d before, %d after", baseGoroutines, runtime.NumGoroutine())

	// Close is synchronous for the file, so the descriptor count is back at once
	if baseFDs >= 0 {
		require.LessOrEqual(t, openFDs(), baseFDs+2, "snapshot file descriptors leaked")
	}
}

// TestSnapshotFileStreamCloseAfterFullRead checks that Close is harmless once the trie data
// has been read to the end, the way snapshot restore uses the stream.
func TestSnapshotFileStreamCloseAfterFullRead(t *testing.T) {
	ledger.ResetForTesting()

	_, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	fpath, _, err := CreateGenesisSnapshot(privateKey, uint32(time.Now().Unix()), "stream read test", t.TempDir())
	require.NoError(t, err)

	stream, err := OpenSnapshotFileStream(fpath)
	require.NoError(t, err)
	n := 0
	for pair := range stream.InChan {
		require.NoError(t, pair.Err)
		n++
	}
	require.Greater(t, n, 0)
	stream.Close()
	stream.Close()
}

// openFDs returns the number of open file descriptors of the process, or -1 where /proc is unavailable.
func openFDs() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}
