package utangle

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

func newSyncStatus() *SyncStatus {
	return &SyncStatus{
		mutex:        sync.RWMutex{},
		whenStarted:  time.Now(),
		perSequencer: make(map[core.ChainID]SequencerSyncStatus),
	}
}

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

// EvidenceIncomingBranch stores branch ID immediately it sees it, before solidification
func (s *SyncStatus) EvidenceIncomingBranch(tx *transaction.Transaction) {
	util.Assertf(tx.IsBranchTransaction(), "must be a branch transaction")

	seqID := tx.SequencerTransactionData().SequencerID
	s.mutex.Lock()
	defer s.mutex.Unlock()

	info := s.perSequencer[seqID]
	if prev := info.latestBranchSeen.Timestamp(); tx.Timestamp().After(prev) {
		info.latestBranchSeen = *tx.ID()
		s.perSequencer[seqID] = info
	}
}

func (s *SyncStatus) EvidenceBookedBranch(tx *transaction.Transaction) {
	util.Assertf(tx.IsBranchTransaction(), "must be a branch transaction")

	seqID := tx.SequencerTransactionData().SequencerID
	s.mutex.Lock()
	defer s.mutex.Unlock()

	info := s.perSequencer[seqID]
	if prev := info.latestBranchBooked.Timestamp(); tx.Timestamp().After(prev) {
		info.latestBranchBooked = *tx.ID()
		s.perSequencer[seqID] = info
	}
}

func (s *SyncStatus) IsSequencerSynced(chainID *core.ChainID) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	info, found := s.perSequencer[*chainID]
	if !found {
		return false
	}

	return info.latestBranchBooked == info.latestBranchSeen
}
