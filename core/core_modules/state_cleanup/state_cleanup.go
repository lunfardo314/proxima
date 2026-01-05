package state_cleanup

import (
	"fmt"
	"math/rand"
	"os"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
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

	// StateCleanup manages periodic state cleanup via snapshot restore
	StateCleanup struct {
		environment
		stateFile        *StateFileManager
		periodSlots      uint32
		windowSlots      uint32
		ttlMinutes       int
		snapshotDir      string
		cleanupRequested atomic.Bool
		cleanupLog       *zap.SugaredLogger // optional separate log for cleanup activity
	}
)

const (
	Name = "state_cleanup"

	defaultPeriodSlots = 8438 // ~24 hours at 10.24 sec/slot
	defaultWindowSlots = 1406 // ~4 hours at 10.24 sec/slot
	defaultTTLMinutes  = 10

	checkPeriod = 60 * time.Second

	defaultLogFile = ".state_cleanup.log"
)

// CleanupRequestedFlag is set when cleanup has been triggered and node should restart
var CleanupRequestedFlag atomic.Bool

// SnapshotFileForRestore is set to the snapshot file path when cleanup is triggered
var SnapshotFileForRestore atomic.Value

// cleanupLogger is a package-level logger for cleanup activity (used during restore before StateCleanup exists)
var cleanupLogger *zap.SugaredLogger

// newCleanupLogger creates a logger that writes to the specified file
func newCleanupLogger(logFile string) *zap.SugaredLogger {
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
func (s *StateCleanup) logCleanup(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.Log().Infof("[%s] %s", Name, msg)
	if s.cleanupLog != nil {
		s.cleanupLog.Info(msg)
	}
}

// logCleanupError logs error to both main log and cleanup log (if configured)
func (s *StateCleanup) logCleanupError(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.Log().Errorf("[%s] %s", Name, msg)
	if s.cleanupLog != nil {
		s.cleanupLog.Error(msg)
	}
}

// Start initializes and starts the state cleanup scheduler
func Start(env environment) {
	if !viper.GetBool("state_cleanup.enable") {
		env.Log().Infof("[%s] is disabled", Name)
		return
	}

	s := &StateCleanup{
		environment: env,
	}

	// Load configuration
	s.periodSlots = uint32(viper.GetInt("state_cleanup.period_slots"))
	if s.periodSlots == 0 {
		s.periodSlots = defaultPeriodSlots
	}

	s.windowSlots = uint32(viper.GetInt("state_cleanup.window_slots"))
	if s.windowSlots == 0 {
		s.windowSlots = defaultWindowSlots
	}

	s.ttlMinutes = viper.GetInt("state_cleanup.ttl_minutes")
	if s.ttlMinutes == 0 {
		s.ttlMinutes = defaultTTLMinutes
	}

	// Use state_cleanup.snapshot_directory if set, otherwise fall back to snapshot.directory
	s.snapshotDir = viper.GetString("state_cleanup.snapshot_directory")
	if s.snapshotDir == "" {
		s.snapshotDir = viper.GetString("snapshot.directory")
	}
	if s.snapshotDir == "" {
		s.snapshotDir = "snapshot"
	}

	// Initialize cleanup log if configured
	logFile := viper.GetString("state_cleanup.log_file")
	if logFile != "" {
		s.cleanupLog = newCleanupLogger(logFile)
		if s.cleanupLog != nil {
			cleanupLogger = s.cleanupLog // set package-level for use during restore
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
		Add("period: %d slots (~%v)", s.periodSlots, time.Duration(s.periodSlots)*ledger.Const.SlotDuration()).
		Add("window: %d slots (~%v)", s.windowSlots, time.Duration(s.windowSlots)*ledger.Const.SlotDuration()).
		Add("TTL: %d minutes", s.ttlMinutes).
		Add("snapshot directory: %s", s.snapshotDir).
		Add("next cleanup slot: %d", s.stateFile.GetNextCleanupSlot())
	if logFile != "" {
		ln.Add("log file: %s", logFile)
	}
	env.Log().Infof("[%s] STARTED\n%s", Name, ln.String())

	// Log startup to cleanup log
	if s.cleanupLog != nil {
		s.cleanupLog.Infof("=== State cleanup scheduler started ===")
		s.cleanupLog.Infof("Period: %d slots (~%v)", s.periodSlots, time.Duration(s.periodSlots)*ledger.Const.SlotDuration())
		s.cleanupLog.Infof("Next cleanup scheduled for slot: %d", s.stateFile.GetNextCleanupSlot())
	}
}

// scheduleNextCleanup calculates and saves the next cleanup slot
func (s *StateCleanup) scheduleNextCleanup() {
	currentSlot := ledger.SlotNow()
	// Add period plus random offset within window
	randomOffset := uint32(rand.Intn(int(s.windowSlots)))
	nextSlot := currentSlot + s.periodSlots + randomOffset

	if err := s.stateFile.SetNextCleanupSlot(nextSlot); err != nil {
		s.logCleanupError("failed to schedule next cleanup: %v", err)
		return
	}

	duration := time.Duration(nextSlot-currentSlot) * ledger.Const.SlotDuration()
	s.logCleanup("next cleanup scheduled for slot %d (in ~%v)", nextSlot, duration)
}

// checkAndTriggerCleanup checks if it's time to clean up and triggers if so
func (s *StateCleanup) checkAndTriggerCleanup() {
	if s.cleanupRequested.Load() {
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

// triggerCleanup initiates the cleanup process
func (s *StateCleanup) triggerCleanup() {
	triggerStart := time.Now()
	s.logCleanup("=== CLEANUP TRIGGERED at slot %d ===", ledger.SlotNow())

	// Find latest snapshot
	snapshotFile, err := FindLatestSnapshot(s.snapshotDir)
	if err != nil {
		s.logCleanupError("no snapshot available: %v - rescheduling", err)
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
	s.cleanupRequested.Store(true)

	// Request graceful shutdown
	s.Stop()
}

// logRestoreMsg logs to main log and cleanup log (if available)
func logRestoreMsg(mainLog global.Logging, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	mainLog.Log().Infof("[%s] %s", Name, msg)
	if cleanupLogger != nil {
		cleanupLogger.Info(msg)
	}
}

// logRestoreError logs error to main log and cleanup log (if available)
func logRestoreError(mainLog global.Logging, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	mainLog.Log().Errorf("[%s] %s", Name, msg)
	if cleanupLogger != nil {
		cleanupLogger.Error(msg)
	}
}

// CheckAndRestoreOnStartup should be called at node startup to check if restore is needed
// Returns true if restore was performed, false otherwise
func CheckAndRestoreOnStartup(log global.Logging) (bool, error) {
	if !viper.GetBool("state_cleanup.enable") {
		return false, nil
	}

	// Initialize cleanup logger if configured (for restore logging)
	logFile := viper.GetString("state_cleanup.log_file")
	if logFile != "" && cleanupLogger == nil {
		cleanupLogger = newCleanupLogger(logFile)
	}

	stateFile, err := NewStateFileManager(DefaultStateFileName)
	if err != nil {
		return false, fmt.Errorf("failed to load state file: %w", err)
	}

	if !stateFile.IsCleanupInProgress() {
		return false, nil
	}

	ttlMinutes := viper.GetInt("state_cleanup.ttl_minutes")
	if ttlMinutes == 0 {
		ttlMinutes = defaultTTLMinutes
	}

	// Check TTL
	if stateFile.IsCleanupTTLExceeded(time.Duration(ttlMinutes) * time.Minute) {
		log.Log().Warnf("[%s] cleanup TTL exceeded, resetting state", Name)
		if cleanupLogger != nil {
			cleanupLogger.Warn("cleanup TTL exceeded, resetting state")
		}
		if err := stateFile.ResetCleanupState(); err != nil {
			return false, fmt.Errorf("failed to reset cleanup state: %w", err)
		}
		return false, nil
	}

	// Perform restore
	snapshotFile := stateFile.GetSnapshotFile()
	if snapshotFile == "" {
		logRestoreError(log, "cleanup in progress but no snapshot file specified")
		if err := stateFile.ResetCleanupState(); err != nil {
			return false, fmt.Errorf("failed to reset cleanup state: %w", err)
		}
		return false, nil
	}

	restoreStart := time.Now()
	logRestoreMsg(log, "=== RESTORE STARTED ===")
	logRestoreMsg(log, "restoring from snapshot: %s", snapshotFile)

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

	logRestoreMsg(log, "restore completed: %d records in %v", stats.TotalRecords, stats.Duration)
	logRestoreMsg(log, "  - transactions: %d", stats.TxCount)
	logRestoreMsg(log, "  - UTXOs: %d", stats.UTXOCount)
	logRestoreMsg(log, "  - chains: %d", stats.ChainCount)
	logRestoreMsg(log, "  - accounts: %d", stats.AccountsCount)

	// Calculate next cleanup slot using constants from the restored snapshot
	periodSlots := uint32(viper.GetInt("state_cleanup.period_slots"))
	if periodSlots == 0 {
		periodSlots = defaultPeriodSlots
	}
	windowSlots := uint32(viper.GetInt("state_cleanup.window_slots"))
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
