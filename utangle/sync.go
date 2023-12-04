package utangle

import (
	"time"

	"github.com/lunfardo314/proxima/core"
)

func (ut *UTXOTangle) SyncStatus() *SyncStatus {
	return ut.syncStatus
}

func SyncWindowDuration() time.Duration {
	return core.TimeSlotDuration() / 2
}

// IsInSyncWindow returns true if latest added transaction (by timestamp) is no more than 1/2 time slot back from now
func (s *SyncStatus) IsInSyncWindow() bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.latestTransactionTSTime.Add(SyncWindowDuration()).After(time.Now())
}

func (s *SyncStatus) storeLatestTxTime(txid *core.TransactionID) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.latestTransactionTSTime = txid.Timestamp().Time()
}

func (s *SyncStatus) WhenStarted() time.Time {
	return s.whenStarted // read-only
}

func (s *SyncStatus) SinceLastPrunedOrphaned() time.Duration {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return time.Since(s.lastPrunedOrphaned)
}

func (s *SyncStatus) SetLastPrunedOrphaned(t time.Time) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.lastPrunedOrphaned = t
}

func (s *SyncStatus) SinceLastCutFinal() time.Duration {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return time.Since(s.lastCutFinal)
}

func (s *SyncStatus) SetLastCutFinal(t time.Time) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.lastCutFinal = t
}
