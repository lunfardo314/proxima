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
	}
)

const (
	Name = "state_cleanup"

	defaultPeriodSlots = 8438 // ~24 hours at 10.24 sec/slot
	defaultWindowSlots = 1406 // ~4 hours at 10.24 sec/slot
	defaultTTLMinutes  = 10

	checkPeriod = 60 * time.Second
)

// CleanupRequestedFlag is set when cleanup has been triggered and node should restart
var CleanupRequestedFlag atomic.Bool

// SnapshotFileForRestore is set to the snapshot file path when cleanup is triggered
var SnapshotFileForRestore atomic.Value

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

	s.snapshotDir = viper.GetString("snapshot.directory")
	if s.snapshotDir == "" {
		s.snapshotDir = "snapshot"
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
	env.Log().Infof("[%s] STARTED\n%s", Name, ln.String())
}

// scheduleNextCleanup calculates and saves the next cleanup slot
func (s *StateCleanup) scheduleNextCleanup() {
	currentSlot := ledger.SlotNow()
	// Add period plus random offset within window
	randomOffset := uint32(rand.Intn(int(s.windowSlots)))
	nextSlot := currentSlot + s.periodSlots + randomOffset

	if err := s.stateFile.SetNextCleanupSlot(nextSlot); err != nil {
		s.Log().Errorf("[%s] failed to schedule next cleanup: %v", Name, err)
		return
	}

	s.Log().Infof("[%s] next cleanup scheduled for slot %d (in ~%v)",
		Name, nextSlot, time.Duration(nextSlot-currentSlot)*ledger.Const.SlotDuration())
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
		s.Log().Infof("[%s] skipping cleanup - node not synced", Name)
		// Reschedule for later
		s.scheduleNextCleanup()
		return
	}

	s.triggerCleanup()
}

// triggerCleanup initiates the cleanup process
func (s *StateCleanup) triggerCleanup() {
	s.Log().Infof("[%s] initiating state cleanup...", Name)

	// Find latest snapshot
	snapshotFile, err := FindLatestSnapshot(s.snapshotDir)
	if err != nil {
		s.Log().Errorf("[%s] no snapshot available: %v - rescheduling", Name, err)
		s.scheduleNextCleanup()
		return
	}

	// Validate snapshot
	if err = ValidateSnapshot(snapshotFile); err != nil {
		s.Log().Errorf("[%s] snapshot validation failed: %v - rescheduling", Name, err)
		s.scheduleNextCleanup()
		return
	}

	// Check permissions
	if err = CheckPermissions(global.MultiStateDBName, snapshotFile); err != nil {
		s.Log().Errorf("[%s] permission check failed: %v - rescheduling", Name, err)
		s.scheduleNextCleanup()
		return
	}

	// Mark cleanup as in progress
	if err := s.stateFile.StartCleanup(snapshotFile); err != nil {
		s.Log().Errorf("[%s] failed to update state file: %v", Name, err)
		return
	}

	s.Log().Infof("[%s] cleanup triggered, snapshot: %s - initiating restart...", Name, snapshotFile)

	// Set global flags for main.go to handle restart
	SnapshotFileForRestore.Store(snapshotFile)
	CleanupRequestedFlag.Store(true)
	s.cleanupRequested.Store(true)

	// Request graceful shutdown
	s.Stop()
}

// CheckAndRestoreOnStartup should be called at node startup to check if restore is needed
// Returns true if restore was performed, false otherwise
func CheckAndRestoreOnStartup(log global.Logging) (bool, error) {
	if !viper.GetBool("state_cleanup.enable") {
		return false, nil
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
		if err := stateFile.ResetCleanupState(); err != nil {
			return false, fmt.Errorf("failed to reset cleanup state: %w", err)
		}
		return false, nil
	}

	// Perform restore
	snapshotFile := stateFile.GetSnapshotFile()
	if snapshotFile == "" {
		log.Log().Errorf("[%s] cleanup in progress but no snapshot file specified", Name)
		if err := stateFile.ResetCleanupState(); err != nil {
			return false, fmt.Errorf("failed to reset cleanup state: %w", err)
		}
		return false, nil
	}

	log.Log().Infof("[%s] cleanup in progress, restoring from %s", Name, snapshotFile)

	// Delete existing database
	if err := DeleteDatabase(global.MultiStateDBName); err != nil {
		return false, fmt.Errorf("failed to delete database: %w", err)
	}

	// Restore from snapshot
	opts := DefaultRestoreOptions()
	opts.Console = os.Stdout
	stats, err := RestoreFromSnapshot(snapshotFile, opts)
	if err != nil {
		return false, fmt.Errorf("restore failed: %w", err)
	}

	log.Log().Infof("[%s] restore completed: %d records in %v", Name, stats.TotalRecords, stats.Duration)

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

	log.Log().Infof("[%s] state cleanup completed successfully, next cleanup at slot %d", Name, nextSlot)

	return true, nil
}
