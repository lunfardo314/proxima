package snapshot_restore

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

// CheckAndDeleteCorruptedDB checks if the database exists and has a restore-in-progress marker.
// If so, it deletes the corrupted database. Returns true if DB was deleted or doesn't exist.
func CheckAndDeleteCorruptedDB(dbPath string, console io.Writer) (bool, error) {
	if console == nil {
		console = io.Discard
	}

	// Check if database directory exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Fprintf(console, "Database does not exist, will create fresh\n")
		return true, nil
	}

	// Try to open the database to check restore-in-progress flag
	stateDb, err := badger_adaptor.OpenBadgerDB(dbPath, badger.DefaultOptions(dbPath).WithReadOnly(true))
	if err != nil {
		// Cannot open DB - likely corrupted, delete it
		fmt.Fprintf(console, "Cannot open database (possibly corrupted): %v, deleting...\n", err)
		if err := os.RemoveAll(dbPath); err != nil {
			return false, fmt.Errorf("failed to delete corrupted database: %w", err)
		}
		return true, nil
	}

	stateStore := badger_adaptor.New(stateDb)
	restoreInProgress := multistate.IsRestoreInProgress(stateStore)
	_ = stateStore.Close()

	if restoreInProgress {
		fmt.Fprintf(console, "Previous restore was interrupted, deleting corrupted database...\n")
		if err := os.RemoveAll(dbPath); err != nil {
			return false, fmt.Errorf("failed to delete corrupted database: %w", err)
		}
		return true, nil
	}

	return false, nil
}

// RestoreFromSnapshot restores the multistate database from a snapshot file
// This is the core restore logic extracted from proxi/snapshot_cmd/restore.go
// Note: Caller should ensure DB is deleted/absent before calling (use CheckAndDeleteCorruptedDB)
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
	fmt.Fprintf(opts.Console, "Upgrade libraries: %d\n", len(kvStream.UpgradeLibraries))

	// Get ledger identity from slot 0 library
	constants, err := kvStream.GetLedgerConstants()
	if err != nil {
		return nil, fmt.Errorf("failed to get ledger constants from snapshot: %w", err)
	}
	ledgerIdentity := ledger.NewLedgerIdentity(constants.GenesisTimeUnix, constants.Description)
	fmt.Fprintf(opts.Console, "Ledger identity: genesis=%d, description=%q\n",
		ledgerIdentity.GenesisTimeUnix, ledgerIdentity.Description)

	start := time.Now()

	// Create fresh database
	stateDb := badger_adaptor.MustCreateOrOpenBadgerDB(
		global.MultiStateDBName,
		badger.DefaultOptions(global.MultiStateDBName),
	)
	stateStore := badger_adaptor.New(stateDb)
	defer func() { _ = stateStore.Close() }()

	// Mark restore as in progress (will be cleared on successful completion)
	initBatch := stateStore.BatchedWriter()
	multistate.WriteRestoreInProgressRecord(initBatch)
	if err = initBatch.Commit(); err != nil {
		return nil, fmt.Errorf("failed to write restore-in-progress marker: %w", err)
	}

	// Store all upgrade libraries in DB partition
	for _, entry := range kvStream.UpgradeLibraries {
		err = multistate.WriteUpgradeLibrary(stateStore, entry.Slot, entry.LibraryYAML)
		if err != nil {
			return nil, fmt.Errorf("failed to write library for slot %d: %w", entry.Slot, err)
		}
		fmt.Fprintf(opts.Console, "  - wrote upgrade library slot %d: %d bytes\n", entry.Slot, len(entry.LibraryYAML))
	}

	// Initialize empty root with minimal ledger identity
	emptyRoot, err := multistate.WriteEmptyRootWithLedgerIdentity(ledgerIdentity, stateStore)
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

	// Write metadata records and clear restore-in-progress marker
	batch = stateStore.BatchedWriter()
	multistate.WriteLatestSlotRecord(batch, kvStream.BranchID.Slot())
	multistate.WriteEarliestSlotRecord(batch, kvStream.BranchID.Slot())
	multistate.WriteRootRecord(batch, kvStream.BranchID, kvStream.RootRecord)
	multistate.DeleteRestoreInProgressRecord(batch) // Clear marker - restore is complete

	if err = batch.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit metadata: %w", err)
	}

	// Verify consistency
	if !ledger.CommitmentModel.EqualCommitments(lastRoot, kvStream.RootRecord.Root) {
		return nil, fmt.Errorf("inconsistency: final root %s != expected root %s",
			lastRoot.String(), kvStream.RootRecord.Root.String())
	}

	stats.Duration = time.Since(start)
	stats.LedgerConstants, err = kvStream.GetLedgerConstants()
	if err != nil {
		return nil, fmt.Errorf("failed to get ledger constants: %w", err)
	}
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
		// Skip temporary snapshot files that are still being written
		if len(name) >= 7 && name[:7] == "__tmp__" {
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

// FindLatestSnapshotInDirs searches for the latest snapshot across multiple directories.
// Directories are searched in order. Returns the most recent snapshot file found.
// This is useful for genesis bootstrap where snapshot may be in working dir or snapshot dir.
func FindLatestSnapshotInDirs(directories ...string) (string, error) {
	var latestFile string
	var latestTime time.Time

	for _, directory := range directories {
		if directory == "" {
			continue
		}

		entries, err := os.ReadDir(directory)
		if err != nil {
			// Directory doesn't exist or can't be read - skip it
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if len(name) < 9 || name[len(name)-9:] != ".snapshot" {
				continue
			}
			// Skip temporary snapshot files that are still being written
			if len(name) >= 7 && name[:7] == "__tmp__" {
				continue
			}

			info, err := entry.Info()
			if err != nil {
				continue
			}

			if info.ModTime().After(latestTime) {
				latestTime = info.ModTime()
				latestFile = filepath.Join(directory, name)
			}
		}
	}

	if latestFile == "" {
		return "", fmt.Errorf("no snapshot files found in directories: %v", directories)
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

	// Get constants from snapshot's slot 0 library
	constants, err := kvStream.GetLedgerConstants()
	if err != nil {
		return fmt.Errorf("cannot get ledger constants from snapshot: %w", err)
	}

	// Check ledger hash matches (both are [32]byte)
	if ledger.L(0).Hash != constants.Hash {
		return fmt.Errorf("snapshot ledger hash mismatch: snapshot is from a different network")
	}

	return nil
}

// GetLatestUpgradeSlotFromSnapshot returns the highest upgrade slot in the snapshot.
// Returns the slot and true if upgrades exist, 0 and false otherwise.
func GetLatestUpgradeSlotFromSnapshot(snapshotPath string) (uint32, bool, error) {
	kvStream, err := multistate.OpenSnapshotFileStream(snapshotPath)
	if err != nil {
		return 0, false, fmt.Errorf("cannot open snapshot: %w", err)
	}
	defer kvStream.Close()

	if len(kvStream.UpgradeLibraries) == 0 {
		return 0, false, nil
	}

	var maxSlot uint32
	for _, entry := range kvStream.UpgradeLibraries {
		if entry.Slot > maxSlot {
			maxSlot = entry.Slot
		}
	}
	return maxSlot, true, nil
}

// GetLatestUpgradeSlotFromDB returns the highest upgrade slot in the database.
// Returns the slot and true if DB exists and has upgrades, 0 and false otherwise.
func GetLatestUpgradeSlotFromDB(dbPath string) (uint32, bool, error) {
	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return 0, false, nil
	}

	// Try to open read-only
	stateDb, err := badger_adaptor.OpenBadgerDB(dbPath, badger.DefaultOptions(dbPath).WithReadOnly(true))
	if err != nil {
		return 0, false, fmt.Errorf("cannot open database: %w", err)
	}
	defer stateDb.Close()

	stateStore := badger_adaptor.New(stateDb)
	latestSlot, found := multistate.GetLatestUpgradeSlot(stateStore)
	return latestSlot, found, nil
}

// CopyFile copies a file from src to dst
func CopyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("cannot open source file %s: %w", src, err)
	}
	defer srcFile.Close()

	// Skip if source and destination are the same
	srcAbs, _ := filepath.Abs(src)
	dstAbs, _ := filepath.Abs(dst)
	if srcAbs == dstAbs {
		return nil
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("cannot create destination file %s: %w", dst, err)
	}
	defer dstFile.Close()

	if _, err = io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("cannot copy file: %w", err)
	}

	return nil
}
