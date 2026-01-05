package state_cleanup

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

const (
	trieCacheSize    = 10_000
	defaultBatchSize = 4_000
)

// RestoreStats holds statistics about the restore operation
type RestoreStats struct {
	TotalRecords    int
	TxCount         int
	UTXOCount       int
	ChainCount      int
	AccountsCount   int
	Duration        time.Duration
	LedgerConstants *ledger.Constants // constants from restored snapshot
}

// RestoreOptions configures the restore operation
type RestoreOptions struct {
	BatchSize int
	Console   io.Writer // optional output for progress
}

// DefaultRestoreOptions returns sensible defaults
func DefaultRestoreOptions() RestoreOptions {
	return RestoreOptions{
		BatchSize: defaultBatchSize,
		Console:   io.Discard,
	}
}

// CheckPermissions verifies the process can perform cleanup operations
func CheckPermissions(dbPath string, snapshotPath string) error {
	// Check snapshot is readable
	if _, err := os.Stat(snapshotPath); err != nil {
		return fmt.Errorf("cannot access snapshot file %s: %w", snapshotPath, err)
	}

	// Check we can write to db parent directory
	parentDir := "."
	if dbPath != "" {
		parentDir = dbPath
	}

	// Try to create a test file
	testFile := parentDir + "/.write_test"
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("cannot write to directory %s: %w", parentDir, err)
	}
	_ = f.Close()
	_ = os.Remove(testFile)

	return nil
}

// DeleteDatabase removes the multistate database directory
func DeleteDatabase(dbPath string) error {
	if dbPath == "" {
		dbPath = global.MultiStateDBName
	}

	// Check if exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil // already gone
	}

	return os.RemoveAll(dbPath)
}

// GetDirectorySize returns the total size of a directory in bytes
func GetDirectorySize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return size, nil
}

// FormatBytes formats bytes as human-readable string (KB, MB, GB)
func FormatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}

// RestoreFromSnapshot restores the multistate database from a snapshot file
// This is the core restore logic extracted from proxi/snapshot_cmd/restore.go
func RestoreFromSnapshot(snapshotPath string, opts RestoreOptions) (*RestoreStats, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultBatchSize
	}
	if opts.Console == nil {
		opts.Console = io.Discard
	}

	// Open snapshot file stream
	kvStream, err := multistate.OpenSnapshotFileStream(snapshotPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open snapshot file: %w", err)
	}
	defer kvStream.Close()

	fmt.Fprintf(opts.Console, "Restoring from snapshot: %s\n", snapshotPath)
	fmt.Fprintf(opts.Console, "Format version: %s\n", kvStream.Header.Version)
	fmt.Fprintf(opts.Console, "Branch ID: %s\n", kvStream.BranchID.String())

	start := time.Now()

	// Create fresh database
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(
		global.MultiStateDBName,
		badger.DefaultOptions(global.MultiStateDBName),
	)
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateStore.Close() }()

	// Initialize empty root with ledger identity
	emptyRoot, err := multistate.CommitEmptyRootWithLedgerIdentity(kvStream.LedgerIDData, stateStore)
	if err != nil {
		return nil, fmt.Errorf("failed to commit empty root: %w", err)
	}

	// Create updatable trie
	trieUpdatable, err := immutable.NewTrieUpdatable(ledger.CommitmentModel, stateStore, emptyRoot, trieCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create trie: %w", err)
	}

	var batch common.KVBatchedWriter
	var inBatch int
	var lastRoot common.VCommitment

	stats := &RestoreStats{}

	// Process all KV pairs from snapshot
	for pair := range kvStream.InChan {
		if util.IsNil(batch) {
			batch = stateStore.BatchedWriter()
		}

		already := trieUpdatable.Update(pair.Key, pair.Value)
		if already {
			return nil, fmt.Errorf("repeating key %s", hex.EncodeToString(pair.Key))
		}
		inBatch++
		stats.TotalRecords++

		// Count by type
		switch pair.Key[0] {
		case multistate.TriePartitionLedgerState:
			if len(pair.Key[1:]) == base.TransactionIDLength {
				stats.TxCount++
			}
			if len(pair.Key[1:]) == base.OutputIDLength {
				stats.UTXOCount++
			}
		case multistate.TriePartitionAccounts:
			stats.AccountsCount++
		case multistate.TriePartitionChainID:
			stats.ChainCount++
		}

		// Commit batch if full
		if inBatch == opts.BatchSize {
			lastRoot = trieUpdatable.Commit(batch)
			if err = batch.Commit(); err != nil {
				return nil, fmt.Errorf("failed to commit batch: %w", err)
			}
			inBatch = 0
			batch = nil
			trieUpdatable, err = immutable.NewTrieUpdatable(ledger.CommitmentModel, stateStore, lastRoot, trieCacheSize)
			if err != nil {
				return nil, fmt.Errorf("failed to create trie after batch: %w", err)
			}
			fmt.Fprintf(opts.Console, "Committed %d records...\n", stats.TotalRecords)
		}
	}

	// Commit remaining records
	if !util.IsNil(batch) {
		lastRoot = trieUpdatable.Commit(batch)
		if err = batch.Commit(); err != nil {
			return nil, fmt.Errorf("failed to commit final batch: %w", err)
		}
	}

	// Write metadata records
	batch = stateStore.BatchedWriter()
	multistate.WriteLatestSlotRecord(batch, kvStream.BranchID.Slot())
	multistate.WriteEarliestSlotRecord(batch, kvStream.BranchID.Slot())
	multistate.WriteRootRecord(batch, kvStream.BranchID, kvStream.RootRecord)

	if err = batch.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit metadata: %w", err)
	}

	// Verify consistency
	if !ledger.CommitmentModel.EqualCommitments(lastRoot, kvStream.RootRecord.Root) {
		return nil, fmt.Errorf("inconsistency: final root %s != expected root %s",
			lastRoot.String(), kvStream.RootRecord.Root.String())
	}

	stats.Duration = time.Since(start)
	stats.LedgerConstants = kvStream.LedgerConstants
	fmt.Fprintf(opts.Console, "Restore complete: %d records in %v\n", stats.TotalRecords, stats.Duration)

	return stats, nil
}

// FindLatestSnapshot finds the most recent snapshot file in the given directory
func FindLatestSnapshot(directory string) (string, error) {
	if directory == "" {
		directory = "snapshot"
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", fmt.Errorf("cannot read snapshot directory %s: %w", directory, err)
	}

	var latestFile string
	var latestTime time.Time

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) < 9 || name[len(name)-9:] != ".snapshot" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latestFile = directory + "/" + name
		}
	}

	if latestFile == "" {
		return "", fmt.Errorf("no snapshot files found in %s", directory)
	}

	return latestFile, nil
}

// ValidateSnapshot checks if a snapshot is compatible with the current ledger
func ValidateSnapshot(snapshotPath string) error {
	kvStream, err := multistate.OpenSnapshotFileStream(snapshotPath)
	if err != nil {
		return fmt.Errorf("cannot open snapshot: %w", err)
	}
	defer kvStream.Close()

	// Check ledger hash matches (both are [32]byte)
	if ledger.Const.Hash != kvStream.LedgerConstants.Hash {
		return fmt.Errorf("snapshot ledger hash mismatch: snapshot is from a different network")
	}

	return nil
}
