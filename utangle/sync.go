package utangle

import (
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

// Evidence methods keeps track of incoming branches and booked branches with the purpose to determine if sequencer is syncing or not.
// If sequencer is syncing, the prune activity is stopped on the tangle and deleting too old in solidifier
// For each sequencer we keep latest booked tx and some amount of latest incoming (maybe not solidified yet).
// This trickery is needed to prevent attack when some node sends invalid yet solidifiable branch to the peer and
// then peer forever thinks it is not-synced. In case branch is dropped and not booked for any reason,
// it is "unevidenced" back and life continues with latest known branch

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

	const keepLastEvidencedIncomingBranches = 5
	if len(info.latestBranchesSeen) > keepLastEvidencedIncomingBranches {
		oldest := info.latestBranchesSeen.Minimum(core.LessTxID)
		info.latestBranchesSeen.Remove(oldest)
	}
	s.perSequencer[seqID] = info
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
		info.latestBranchesSeen.ForEach(func(txid core.TransactionID) bool {
			return true
		})
		if info.latestBranchBooked.Timestamp() != s.latestSeenBranchTimestamp(seqID) {
			return false
		}
	}
	return true
}
