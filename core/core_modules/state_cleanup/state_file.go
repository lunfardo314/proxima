package state_cleanup

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

const (
	DefaultStateFileName = ".state_cleanup.json"
)

// StateFile holds the cleanup state persisted to disk
type StateFile struct {
	LastCleanupCompleted *time.Time `json:"last_cleanup_completed,omitempty"`
	NextCleanupSlot      uint32     `json:"next_cleanup_slot,omitempty"`
	CleanupInProgress    bool       `json:"cleanup_in_progress"`
	CleanupStartedAt     *time.Time `json:"cleanup_started_at,omitempty"`
	SnapshotFile         string     `json:"snapshot_file,omitempty"`
}

// StateFileManager manages the state file on disk
type StateFileManager struct {
	mu       sync.Mutex
	filePath string
	state    StateFile
}

// NewStateFileManager creates or loads an existing state file
func NewStateFileManager(filePath string) (*StateFileManager, error) {
	m := &StateFileManager{
		filePath: filePath,
	}

	if err := m.load(); err != nil {
		if os.IsNotExist(err) {
			// Create new empty state
			m.state = StateFile{}
			if err := m.save(); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	return m, nil
}

// load reads the state file from disk
func (m *StateFileManager) load() error {
	data, err := os.ReadFile(m.filePath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &m.state)
}

// save writes the state file to disk
func (m *StateFileManager) save() error {
	data, err := json.MarshalIndent(&m.state, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically: write to temp file, then rename
	tmpFile := m.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, m.filePath)
}

// GetState returns a copy of the current state
func (m *StateFileManager) GetState() StateFile {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// IsCleanupInProgress returns true if cleanup was started but not completed
func (m *StateFileManager) IsCleanupInProgress() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.CleanupInProgress
}

// GetSnapshotFile returns the snapshot file path if cleanup is in progress
func (m *StateFileManager) GetSnapshotFile() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.SnapshotFile
}

// GetCleanupStartedAt returns when cleanup started (nil if not in progress)
func (m *StateFileManager) GetCleanupStartedAt() *time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.CleanupStartedAt
}

// GetNextCleanupSlot returns the scheduled next cleanup slot
func (m *StateFileManager) GetNextCleanupSlot() uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.NextCleanupSlot
}

// SetNextCleanupSlot schedules the next cleanup slot
func (m *StateFileManager) SetNextCleanupSlot(slot uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state.NextCleanupSlot = slot
	return m.save()
}

// StartCleanup marks cleanup as in progress
func (m *StateFileManager) StartCleanup(snapshotFile string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.state.CleanupInProgress = true
	m.state.CleanupStartedAt = &now
	m.state.SnapshotFile = snapshotFile
	return m.save()
}

// CompleteCleanup marks cleanup as completed and schedules next
func (m *StateFileManager) CompleteCleanup(nextSlot uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.state.CleanupInProgress = false
	m.state.CleanupStartedAt = nil
	m.state.SnapshotFile = ""
	m.state.LastCleanupCompleted = &now
	m.state.NextCleanupSlot = nextSlot
	return m.save()
}

// ResetCleanupState resets cleanup state (used when TTL exceeded)
func (m *StateFileManager) ResetCleanupState() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state.CleanupInProgress = false
	m.state.CleanupStartedAt = nil
	m.state.SnapshotFile = ""
	return m.save()
}

// IsCleanupTTLExceeded checks if cleanup has been in progress longer than TTL
func (m *StateFileManager) IsCleanupTTLExceeded(ttl time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.state.CleanupInProgress || m.state.CleanupStartedAt == nil {
		return false
	}
	return time.Since(*m.state.CleanupStartedAt) > ttl
}
