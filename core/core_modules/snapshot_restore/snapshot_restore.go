package snapshot_restore

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core/core_modules/snapshot"
	syncmod "github.com/lunfardo314/proxima/core/core_modules/forward_sync"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	environment interface {
		global.NodeGlobal
		IsSynced() bool
	}

	// SnapshotRestore manages periodic state restore from snapshots
	SnapshotRestore struct {
		environment
		stateFile         *StateFileManager
		periodSlots       uint32
		windowSlots       uint32
		ttlMinutes        int
		snapshotDir       string
		restoreRequested  atomic.Bool
		restoreLog        *zap.SugaredLogger // optional separate log for restore activity
	}
)

const (
	Name = "snapshot_restore"

	defaultPeriodSlots = 8438 // ~24 hours at 10.24 sec/slot
	defaultWindowSlots = 1406 // ~4 hours at 10.24 sec/slot
	defaultTTLMinutes  = 10

	// minSnapshotAgeSlots: when downloading, prefer a snapshot at least this many
	// slots old (relative to the serving source's current slot). Restoring from a
	// younger snapshot makes the sequencer wait out its start guard
	// (ensureNotTooCloseToSnapshot in sequencer.go, also 64) and is pointless when an
	// old-enough one is available — e.g. when a peer has snapshot.always_serve set
	// and bypasses the server-side gate. Keep in sync with the sequencer guard and
	// the API serve gate (api/server: snapshotServeMinAgeSlots).
	minSnapshotAgeSlots = 64

	checkPeriod = 60 * time.Second

	defaultLogFile = ".snapshot_restore.log"
)

// CleanupRequestedFlag is set when cleanup has been triggered and node should restart
var CleanupRequestedFlag atomic.Bool

// SnapshotFileForRestore is set to the snapshot file path when cleanup is triggered
var SnapshotFileForRestore atomic.Value

// restoreLogger is a package-level logger for cleanup activity (used during restore before StateCleanup exists)
var restoreLogger *zap.SugaredLogger

// newRestoreLogger creates a logger that writes to the specified file
func newRestoreLogger(logFile string) *zap.SugaredLogger {
	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapcore.InfoLevel),
		Development:      false,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{logFile},
		ErrorOutputPaths: []string{logFile},
		DisableCaller:    true,
	}
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(global.TimeLayoutDefault)

	log, err := cfg.Build()
	if err != nil {
		return nil
	}
	return log.Sugar().Named(Name)
}

// logCleanup logs to both main log and cleanup log (if configured)
func (s *SnapshotRestore) logCleanup(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.Log().Infof("[%s] %s", Name, msg)
	if s.restoreLog != nil {
		s.restoreLog.Info(msg)
	}
}

// logCleanupError logs error to both main log and cleanup log (if configured)
func (s *SnapshotRestore) logCleanupError(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.Log().Errorf("[%s] %s", Name, msg)
	if s.restoreLog != nil {
		s.restoreLog.Error(msg)
	}
}

// Start initializes and starts the snapshot restore scheduler
func Start(env environment) {
	enableValue := viper.GetBool("snapshot_restore.enable")
	if !enableValue {
		// Log detailed diagnostics about why snapshot_restore is not starting
		env.Log().Infof("[%s] NOT STARTED: snapshot_restore.enable = %v", Name, enableValue)
		// Check if the key exists at all in config
		if !viper.IsSet("snapshot_restore.enable") {
			env.Log().Infof("[%s] (config key 'snapshot_restore.enable' is not set in config file)", Name)
		}
		// Log what snapshot_restore config values were found (if any)
		if viper.IsSet("snapshot_restore") {
			env.Log().Infof("[%s] Found snapshot_restore section with: period_slots=%d, window_slots=%d, snapshot_directory=%q",
				Name,
				viper.GetInt("snapshot_restore.period_slots"),
				viper.GetInt("snapshot_restore.window_slots"),
				viper.GetString("snapshot_restore.snapshot_directory"))
		} else {
			env.Log().Infof("[%s] (no 'snapshot_restore' section found in config file)", Name)
		}
		return
	}

	s := &SnapshotRestore{
		environment: env,
	}

	// Load configuration
	s.periodSlots = uint32(viper.GetInt("snapshot_restore.period_slots"))
	if s.periodSlots == 0 {
		s.periodSlots = defaultPeriodSlots
	}

	s.windowSlots = uint32(viper.GetInt("snapshot_restore.window_slots"))
	if s.windowSlots == 0 {
		s.windowSlots = defaultWindowSlots
	}

	s.ttlMinutes = viper.GetInt("snapshot_restore.ttl_minutes")
	if s.ttlMinutes == 0 {
		s.ttlMinutes = defaultTTLMinutes
	}

	// Use snapshot_restore.snapshot_directory if explicitly set, otherwise use the shared snapshot.directory
	s.snapshotDir = viper.GetString("snapshot_restore.snapshot_directory")
	if s.snapshotDir == "" {
		s.snapshotDir = snapshot.SnapshotDirectory()
	}

	// Initialize cleanup log if configured
	logFile := viper.GetString("snapshot_restore.log_file")
	if logFile != "" {
		s.restoreLog = newRestoreLogger(logFile)
		if s.restoreLog != nil {
			restoreLogger = s.restoreLog // set package-level for use during restore
			env.Log().Infof("[%s] cleanup activity logging to: %s", Name, logFile)
		}
	}

	// Initialize state file
	var err error
	s.stateFile, err = NewStateFileManager(DefaultStateFileName)
	if err != nil {
		env.Log().Errorf("[%s] failed to initialize state file: %v", Name, err)
		return
	}

	// Schedule next cleanup if not already scheduled
	if s.stateFile.GetNextCleanupSlot() == 0 {
		s.scheduleNextCleanup()
	}

	// Start scheduler loop
	env.RepeatInBackground(Name, checkPeriod, func() bool {
		s.checkAndTriggerCleanup()
		return true
	}, true)

	ln := lines.New("          ").
		Add("period: %d slots (~%v)", s.periodSlots, time.Duration(s.periodSlots)*ledger.L(0).SlotDuration()).
		Add("window: %d slots (~%v)", s.windowSlots, time.Duration(s.windowSlots)*ledger.L(0).SlotDuration()).
		Add("TTL: %d minutes", s.ttlMinutes).
		Add("snapshot directory: %s", s.snapshotDir).
		Add("next cleanup slot: %d", s.stateFile.GetNextCleanupSlot())
	if logFile != "" {
		ln.Add("log file: %s", logFile)
	}
	env.Log().Infof("[%s] STARTED\n%s", Name, ln.String())

	// Log startup to cleanup log
	if s.restoreLog != nil {
		s.restoreLog.Infof("=== State cleanup scheduler started ===")
		s.restoreLog.Infof("Period: %d slots (~%v)", s.periodSlots, time.Duration(s.periodSlots)*ledger.L(0).SlotDuration())
		s.restoreLog.Infof("Next cleanup scheduled for slot: %d", s.stateFile.GetNextCleanupSlot())
	}
}

// scheduleNextCleanup calculates and saves the next cleanup slot
func (s *SnapshotRestore) scheduleNextCleanup() {
	currentSlot := ledger.SlotNow()
	// Add period plus random offset within window
	randomOffset := uint32(rand.Intn(int(s.windowSlots)))
	nextSlot := currentSlot + s.periodSlots + randomOffset

	if err := s.stateFile.SetNextCleanupSlot(nextSlot); err != nil {
		s.logCleanupError("failed to schedule next cleanup: %v", err)
		return
	}

	duration := time.Duration(nextSlot-currentSlot) * ledger.L(0).SlotDuration()
	s.logCleanup("next cleanup scheduled for slot %d (in ~%v)", nextSlot, duration)
}

// checkAndTriggerCleanup checks if it's time to clean up and triggers if so
func (s *SnapshotRestore) checkAndTriggerCleanup() {
	if s.restoreRequested.Load() {
		return // already triggered
	}

	nextSlot := s.stateFile.GetNextCleanupSlot()
	if nextSlot == 0 {
		s.scheduleNextCleanup()
		return
	}

	currentSlot := ledger.TimeNow().Slot
	if currentSlot < nextSlot {
		return // not time yet
	}

	if !s.IsSynced() {
		s.logCleanup("skipping cleanup - node not synced, rescheduling")
		s.scheduleNextCleanup()
		return
	}

	s.triggerCleanup()
}

// triggerCleanup initiates the cleanup process.
// Skips cleanup if no snapshots are available in the snapshot directory.
func (s *SnapshotRestore) triggerCleanup() {
	triggerStart := time.Now()
	s.logCleanup("=== CLEANUP TRIGGERED at slot %d ===", ledger.SlotNow())

	// Find latest snapshot in the configured snapshot directory
	snapshotFile, err := FindLatestSnapshot(s.snapshotDir)
	if err != nil {
		s.logCleanupError("no snapshot available in %s: %v - rescheduling", s.snapshotDir, err)
		s.scheduleNextCleanup()
		return
	}
	s.logCleanup("found snapshot: %s", snapshotFile)

	// Validate snapshot
	if err = ValidateSnapshot(snapshotFile); err != nil {
		s.logCleanupError("snapshot validation failed: %v - rescheduling", err)
		s.scheduleNextCleanup()
		return
	}
	s.logCleanup("snapshot validated successfully")

	// Check permissions
	if err = CheckPermissions(global.MultiStateDBName, snapshotFile); err != nil {
		s.logCleanupError("permission check failed: %v - rescheduling", err)
		s.scheduleNextCleanup()
		return
	}

	// Mark cleanup as in progress
	if err := s.stateFile.StartCleanup(snapshotFile); err != nil {
		s.logCleanupError("failed to update state file: %v", err)
		return
	}

	s.logCleanup("cleanup prepared in %v, initiating restart...", time.Since(triggerStart))

	// Set global flags for main.go to handle restart
	SnapshotFileForRestore.Store(snapshotFile)
	CleanupRequestedFlag.Store(true)
	s.restoreRequested.Store(true)

	// Request graceful shutdown
	s.Stop()
}

// logRestoreMsg logs to main log and cleanup log (if available)
func logRestoreMsg(mainLog global.Logging, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	mainLog.Log().Infof("[%s] %s", Name, msg)
	if restoreLogger != nil {
		restoreLogger.Info(msg)
	}
}

// logRestoreError logs error to main log and cleanup log (if available)
func logRestoreError(mainLog global.Logging, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	mainLog.Log().Errorf("[%s] %s", Name, msg)
	if restoreLogger != nil {
		restoreLogger.Error(msg)
	}
}

// tryDownloadRemoteSnapshot downloads a snapshot from the shared top-level 'sources' list
// (trusted node API endpoints, the same list forward-sync uses), preferring the newest one
// that is at least minSnapshotAgeSlots old so the sequencer does not have to wait out its
// start guard. A peer snapshot still takes priority over the local one. Each snapshot is
// aged against the serving source's own current slot (the local ledger clock is not yet
// initialized during startup restore), which also defends against a peer that bypasses the
// server-side gate via snapshot.always_serve. Tries candidates in order (old-enough newest
// first, then younger as a last resort) until a download succeeds. Returns the downloaded
// file path, or empty string if there are no sources or every download failed — in which
// case the caller falls back to the local snapshot directory. Independent of forward-sync's
// own activation (whether 'sources' drives catch-up) and of snapshot production (snapshot.enable).
func tryDownloadRemoteSnapshot(log global.Logging, snapshotDir string) string {
	sourceURLs := viper.GetStringSlice("sources")
	if len(sourceURLs) == 0 {
		return ""
	}

	selfAPIPort := viper.GetInt("api.port")

	// query all reachable sources for their snapshot info (a source that refuses
	// to serve via the snapshot serve gate returns an error here and is skipped)
	type remoteSource struct {
		url   string
		c     *client.APIClient
		slot  uint32
		ageOK bool // snapshot is >= minSnapshotAgeSlots old per the source's current slot
	}
	var sources []remoteSource
	for _, url := range sourceURLs {
		if syncmod.IsSelfURL(url, selfAPIPort) {
			continue
		}
		c := client.NewWithGoogleDNS(url, 15*time.Second)
		info, err := c.GetSnapshotInfo()
		if err != nil {
			log.Log().Warnf("[%s] snapshot info from %s: %v", Name, url, err)
			continue
		}
		// age against the source's own current slot, not the local clock (ledger not
		// yet initialized during restore). Same notion of age as the server serve gate.
		ageOK := false
		if si, err := c.GetSyncInfo(); err == nil && si.CurrentSlot >= info.Slot+minSnapshotAgeSlots {
			ageOK = true
		}
		log.Log().Infof("[%s] remote snapshot at %s: slot %d, size %d, old enough: %v", Name, url, info.Slot, info.FileSize, ageOK)
		sources = append(sources, remoteSource{url: url, c: c, slot: info.Slot, ageOK: ageOK})
	}
	if len(sources) == 0 {
		return ""
	}

	// old-enough first (so the sequencer does not wait its start guard), newest within
	// each group; a peer snapshot still beats the local one.
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].ageOK != sources[j].ageOK {
			return sources[i].ageOK
		}
		return sources[i].slot > sources[j].slot
	})
	if !sources[0].ageOK {
		log.Log().Warnf("[%s] no source has a snapshot >= %d slots old; using the newest available (sequencer may wait its start guard)", Name, minSnapshotAgeSlots)
	}

	// try downloads in order; fall back to local only if all of them fail
	for _, s := range sources {
		log.Log().Infof("[%s] downloading snapshot (slot %d, old enough: %v) from %s...", Name, s.slot, s.ageOK, s.url)
		downloaded, err := s.c.DownloadSnapshot(snapshotDir)
		if err != nil {
			log.Log().Errorf("[%s] failed to download snapshot from %s: %v", Name, s.url, err)
			continue
		}
		log.Log().Infof("[%s] snapshot downloaded: %s", Name, downloaded)
		return downloaded
	}
	log.Log().Warnf("[%s] all snapshot downloads failed, will use local snapshot if available", Name)
	return ""
}

// CheckAndRestoreOnStartup should be called at node startup to check if restore is needed.
// This function handles two scenarios:
// 1. Genesis bootstrap: DB is missing, find and restore from any available snapshot (including genesis.snapshot)
// 2. Periodic cleanup: snapshot_restore is enabled and cleanup was in progress
// Returns true if restore was performed, false otherwise
func CheckAndRestoreOnStartup(log global.Logging) (bool, error) {
	// First, check if DB exists/is valid - this is independent of snapshot_restore.enable
	dbNeedsRestore, err := CheckAndDeleteCorruptedDB(global.MultiStateDBName, os.Stdout)
	if err != nil {
		return false, fmt.Errorf("failed to check database state: %w", err)
	}

	// Too-old / divergent state: DB present and valid, but far behind the network.
	// Determined by reading the DB's latest committed slot directly (the live node's
	// IsSynced is unreliable on a stale fork) and comparing to the newest remote
	// snapshot. If too old, treat it exactly like a corrupted DB: the select-snapshot /
	// refuse-if-none / delete-DB / restore path below replaces it with the fresher
	// snapshot. checkStateTooOldDownload has already downloaded that snapshot.
	// See too_old_recovery.go.
	var tooOldSnapshot string
	if !dbNeedsRestore {
		f, err := checkStateTooOldDownload(log)
		if err != nil {
			// Scenario 6b: too old behind a live network, no snapshot to adopt — refuse and
			// shut down with a clear message rather than start on an unsyncable state.
			return false, err
		}
		if f != "" {
			dbNeedsRestore = true
			tooOldSnapshot = f
		}
	}

	snapshotRestoreEnabled := viper.GetBool("snapshot_restore.enable")

	// If DB is fine and snapshot_restore is disabled, nothing to do
	if !dbNeedsRestore && !snapshotRestoreEnabled {
		log.Log().Infof("[%s] CheckAndRestoreOnStartup: DB exists and snapshot_restore is disabled, skipping", Name)
		return false, nil
	}

	// Initialize restore logger if configured (for restore logging)
	logFile := viper.GetString("snapshot_restore.log_file")
	if logFile != "" && restoreLogger == nil {
		restoreLogger = newRestoreLogger(logFile)
	}

	// Load state file (may contain in-progress cleanup info)
	stateFile, err := NewStateFileManager(DefaultStateFileName)
	if err != nil {
		return false, fmt.Errorf("failed to load state file: %w", err)
	}

	cleanupInProgress := stateFile.IsCleanupInProgress()

	// If DB is fine and no cleanup in progress, nothing to do
	if !dbNeedsRestore && !cleanupInProgress {
		log.Log().Infof("[%s] CheckAndRestoreOnStartup: DB exists and no pending cleanup, skipping", Name)
		return false, nil
	}

	// Log what we're doing
	if dbNeedsRestore {
		log.Log().Infof("[%s] CheckAndRestoreOnStartup: DB missing/corrupted, will restore from snapshot", Name)
	} else {
		log.Log().Infof("[%s] CheckAndRestoreOnStartup: continuing pending cleanup restore", Name)
	}

	ttlMinutes := viper.GetInt("snapshot_restore.ttl_minutes")
	if ttlMinutes == 0 {
		ttlMinutes = defaultTTLMinutes
	}

	// Check TTL only if cleanup was in progress (not for missing DB case)
	if cleanupInProgress && stateFile.IsCleanupTTLExceeded(time.Duration(ttlMinutes)*time.Minute) {
		log.Log().Warnf("[%s] cleanup TTL exceeded, resetting state", Name)
		if restoreLogger != nil {
			restoreLogger.Warn("cleanup TTL exceeded, resetting state")
		}
		if err := stateFile.ResetCleanupState(); err != nil {
			return false, fmt.Errorf("failed to reset cleanup state: %w", err)
		}
		// Still need to restore if DB is missing
		if !dbNeedsRestore {
			return false, nil
		}
	}

	// Get snapshot file - first try state file (in-progress cleanup), then try remote
	// download from sync sources, then find latest in local snapshot directory
	snapshotFile := stateFile.GetSnapshotFile()
	if snapshotFile == "" && tooOldSnapshot != "" {
		// too-old recovery already downloaded a fresher snapshot
		snapshotFile = tooOldSnapshot
		logRestoreMsg(log, "using fresher snapshot for too-old state: %s", snapshotFile)
	}
	if snapshotFile == "" {
		snapshotDir := snapshot.SnapshotDirectory()

		// Try to download a newer snapshot from remote sync sources
		if downloaded := tryDownloadRemoteSnapshot(log, snapshotDir); downloaded != "" {
			snapshotFile = downloaded
			logRestoreMsg(log, "using downloaded snapshot: %s", snapshotFile)
		}

		// Fall back to local snapshot directory
		if snapshotFile == "" {
			snapshotFile, err = FindLatestSnapshot(snapshotDir)
			if err != nil {
				logRestoreError(log, "no snapshot found in %s: %v", snapshotDir, err)
				if cleanupInProgress {
					if err := stateFile.ResetCleanupState(); err != nil {
						return false, fmt.Errorf("failed to reset cleanup state: %w", err)
					}
				}
				return false, fmt.Errorf("database missing/corrupted and no snapshot available in '%s'", snapshotDir)
			}
		}
		logRestoreMsg(log, "found snapshot for restore: %s", snapshotFile)
	}

	// Get absolute path for clear logging
	snapshotFileAbs, _ := filepath.Abs(snapshotFile)

	// Check upgrade slot compatibility: snapshot must have same latest upgrade slot as DB
	// This invalidates stale snapshots when a ledger upgrade has been activated
	if !dbNeedsRestore {
		// DB exists - compare upgrade slots
		snapshotUpgradeSlot, snapshotHasUpgrade, err := GetLatestUpgradeSlotFromSnapshot(snapshotFile)
		if err != nil {
			logRestoreError(log, "failed to read upgrade slot from snapshot: %v", err)
			// Continue anyway - the restore may still work
		} else {
			dbUpgradeSlot, dbHasUpgrade, err := GetLatestUpgradeSlotFromDB(global.MultiStateDBName)
			if err != nil {
				logRestoreError(log, "failed to read upgrade slot from DB: %v", err)
				// Continue anyway
			} else if snapshotHasUpgrade && dbHasUpgrade && snapshotUpgradeSlot != dbUpgradeSlot {
				// Upgrade slot mismatch - snapshot is from before/after an upgrade
				logRestoreMsg(log, "=== SNAPSHOT INVALIDATED ===")
				logRestoreMsg(log, "Snapshot upgrade slot (%d) != DB upgrade slot (%d)", snapshotUpgradeSlot, dbUpgradeSlot)
				logRestoreMsg(log, "Ledger upgrade has occurred - deleting stale snapshot")

				// Delete the stale snapshot file
				if err := os.Remove(snapshotFile); err != nil {
					logRestoreMsg(log, "warning: failed to delete snapshot: %v", err)
				} else {
					logRestoreMsg(log, "deleted stale snapshot: %s", snapshotFileAbs)
				}

				// Also clean up any snapshots in working directory
				if err := util.PurgeFilesInDirectory(".", "*.snapshot", 0); err != nil {
					logRestoreMsg(log, "warning: failed to cleanup snapshots in working dir: %v", err)
				}

				// Reset state file so cleanup cycle starts fresh
				if err := stateFile.ResetCleanupState(); err != nil {
					return false, fmt.Errorf("failed to reset cleanup state: %w", err)
				}
				logRestoreMsg(log, "state cleanup reset - will start fresh after upgrade")

				// Return without restoring - node will continue with existing DB
				return false, nil
			}
		}
	}

	restoreStart := time.Now()
	logRestoreMsg(log, "=== RESTORE STARTED ===")
	logRestoreMsg(log, "snapshot file: %s", snapshotFileAbs)

	// Get database size before cleanup
	dbSizeBefore, _ := GetDirectorySize(global.MultiStateDBName)
	logRestoreMsg(log, "database size before: %s", FormatBytes(dbSizeBefore))

	// Delete existing database
	deleteStart := time.Now()
	if err := DeleteDatabase(global.MultiStateDBName); err != nil {
		return false, fmt.Errorf("failed to delete database: %w", err)
	}
	logRestoreMsg(log, "deleted old database in %v", time.Since(deleteStart))

	// Restore from snapshot
	opts := DefaultRestoreOptions()
	opts.Console = os.Stdout
	stats, err := RestoreFromSnapshot(snapshotFile, opts)
	if err != nil {
		logRestoreError(log, "restore failed: %v", err)
		return false, fmt.Errorf("restore failed: %w", err)
	}

	// Get database size after restore
	dbSizeAfter, _ := GetDirectorySize(global.MultiStateDBName)
	dbSizeDelta := dbSizeBefore - dbSizeAfter

	logRestoreMsg(log, "restore completed: %d records in %v", stats.TotalRecords, stats.Duration)
	logRestoreMsg(log, "  - transactions: %d", stats.TxCount)
	logRestoreMsg(log, "  - UTXOs: %d", stats.UTXOCount)
	logRestoreMsg(log, "  - chains: %d", stats.ChainCount)
	logRestoreMsg(log, "  - accounts: %d", stats.AccountsCount)
	logRestoreMsg(log, "database size after: %s", FormatBytes(dbSizeAfter))
	if dbSizeBefore > 0 {
		logRestoreMsg(log, "database size reduced by: %s (%.1f%%)", FormatBytes(dbSizeDelta), float64(dbSizeDelta)*100/float64(dbSizeBefore))
	}

	// Calculate next cleanup slot using constants from the restored snapshot
	periodSlots := uint32(viper.GetInt("snapshot_restore.period_slots"))
	if periodSlots == 0 {
		periodSlots = defaultPeriodSlots
	}
	windowSlots := uint32(viper.GetInt("snapshot_restore.window_slots"))
	if windowSlots == 0 {
		windowSlots = defaultWindowSlots
	}
	// Use restored snapshot's ledger constants since global ledger.Const isn't initialized yet
	currentSlot := stats.LedgerConstants.LedgerTimeFromClockTime(time.Now()).Slot
	randomOffset := uint32(rand.Intn(int(windowSlots)))
	nextSlot := currentSlot + periodSlots + randomOffset

	// Mark cleanup complete
	if err := stateFile.CompleteCleanup(nextSlot); err != nil {
		return false, fmt.Errorf("failed to complete cleanup state: %w", err)
	}

	totalDuration := time.Since(restoreStart)
	logRestoreMsg(log, "=== CLEANUP COMPLETED in %v ===", totalDuration)
	logRestoreMsg(log, "next cleanup scheduled for slot %d", nextSlot)

	return true, nil
}
