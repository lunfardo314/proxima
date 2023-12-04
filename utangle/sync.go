package utangle

import (
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
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
func (s *SyncStatus) EvidenceIncomingBranch(txid *core.TransactionID, seqID core.ChainID) {
	util.Assertf(txid.BranchFlagON(), "must be a branch transaction")

	s.mutex.Lock()
	defer s.mutex.Unlock()

	info, found := s.perSequencer[seqID]
	if !found {
		info.latestBranchesSeen = set.New[core.TransactionID]()
	}

	latest := s.latestSeenBranchTimestamp(seqID)
	if latest.Before(txid.Timestamp()) {
		info.latestBranchesSeen.Insert(*txid)
	}

	const keepLastEvidencedIncomingBranches = 10
	if len(info.latestBranchesSeen) > keepLastEvidencedIncomingBranches {
		oldest := info.latestBranchesSeen.Minimum(core.LessTxID)
		info.latestBranchesSeen.Remove(oldest)
	}
}

func (s *SyncStatus) UnEvidenceIncomingBranch(txid *core.TransactionID) {
	util.Assertf(txid.BranchFlagON(), "must be a branch transaction")

	s.mutex.Lock()
	defer s.mutex.Unlock()

	for _, info := range s.perSequencer {
		delete(info.latestBranchesSeen, *txid) // a bit suboptimal, but we do not want to search for the whole tx
	}
}

func (s *SyncStatus) latestSeenBranchTimestamp(seqID core.ChainID) (ret core.LogicalTime) {
	if info, found := s.perSequencer[seqID]; found {
		latest := info.latestBranchesSeen.Maximum(core.LessTxID)
		ret = latest.Timestamp()
	}
	return
}

func (s *SyncStatus) EvidenceBookedBranch(txid *core.TransactionID, seqID core.ChainID) {
	util.Assertf(txid.BranchFlagON(), "must be a branch transaction")

	s.mutex.Lock()
	defer s.mutex.Unlock()

	info := s.perSequencer[seqID]
	if prev := info.latestBranchBooked.Timestamp(); txid.Timestamp().After(prev) {
		info.latestBranchBooked = *txid
		s.perSequencer[seqID] = info
	}
}

func (s *SyncStatus) AllSequencersSynced() bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	for seqID, info := range s.perSequencer {
		fmt.Printf(">>>>>>>>>>>>>>> seqID: %s\n", seqID.Short())
		info.latestBranchesSeen.ForEach(func(txid core.TransactionID) bool {
			fmt.Printf(">>>>>>>>>>>>>>> seqID: %s\n", txid.StringShort())
			return true
		})
		if info.latestBranchBooked.Timestamp() != s.latestSeenBranchTimestamp(seqID) {
			return false
		}
	}
	return true
}
