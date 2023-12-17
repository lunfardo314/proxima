package utangle

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func newSyncData() *SyncData {
	return &SyncData{
		mutex:        sync.RWMutex{},
		StartTime:    time.Now(),
		PerSequencer: make(map[core.ChainID]SequencerSyncStatus),
	}
}

func (ut *UTXOTangle) SyncData() *SyncData {
	return ut.syncData
}

func (s *SyncData) GetSyncInfo() (ret SyncInfo) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	ret.Synced = s.IsSynced()
	ret.InSyncWindow = s.isInSyncWindow()
	ret.PerSequencer = make(map[core.ChainID]SequencerSyncInfo)
	for seqID := range s.PerSequencer {
		ret.PerSequencer[seqID] = s.sequencerSyncInfo(seqID)
	}
	return
}

func (s *SyncData) storeLatestTxTime(txid *core.TransactionID) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.latestTransactionTSTime = txid.Timestamp().Time()
}

func (s *SyncData) WhenStarted() time.Time {
	return s.StartTime // read-only
}

func (s *SyncData) SinceLastPrunedOrphaned() time.Duration {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return time.Since(s.lastPrunedOrphaned)
}

func (s *SyncData) SetLastPrunedOrphaned(t time.Time) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.lastPrunedOrphaned = t
}

func (s *SyncData) SinceLastCutFinal() time.Duration {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return time.Since(s.lastCutFinal)
}

func (s *SyncData) SetLastCutFinal(t time.Time) {
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
func (s *SyncData) EvidenceIncomingBranch(txid *core.TransactionID, seqID core.ChainID) {
	util.Assertf(txid.IsBranchTransaction(), "must be a branch transaction")

	s.mutex.Lock()
	defer s.mutex.Unlock()

	info, found := s.PerSequencer[seqID]
	if !found {
		info.latestBranchesSeen = set.New[core.TransactionID]()
	}

	latest := info.latestBranchesSeen.Maximum(core.LessTxID)
	if latest.Timestamp().Before(txid.Timestamp()) {
		info.latestBranchesSeen.Insert(*txid)
	}

	const keepLastEvidencedIncomingBranches = 5
	if len(info.latestBranchesSeen) > keepLastEvidencedIncomingBranches {
		oldest := info.latestBranchesSeen.Minimum(core.LessTxID)
		info.latestBranchesSeen.Remove(oldest)
	}
	s.PerSequencer[seqID] = info
}

func (s *SyncData) UnEvidenceIncomingBranch(txid core.TransactionID) {
	util.Assertf(txid.IsBranchTransaction(), "must be a branch transaction")

	s.mutex.Lock()
	defer s.mutex.Unlock()

	for _, info := range s.PerSequencer {
		delete(info.latestBranchesSeen, txid) // a bit suboptimal, but we do not want to search for the whole tx
	}
}

func (s *SyncData) EvidenceBookedBranch(txid *core.TransactionID, seqID core.ChainID) {
	util.Assertf(txid.IsBranchTransaction(), "must be a branch transaction")

	s.mutex.Lock()
	defer s.mutex.Unlock()

	info := s.PerSequencer[seqID]
	if prev := info.latestBranchBooked.Timestamp(); txid.Timestamp().After(prev) {
		info.latestBranchBooked = *txid
		s.PerSequencer[seqID] = info
	}
}

func (s *SyncData) sequencerSyncInfo(seqID core.ChainID) SequencerSyncInfo {
	info, ok := s.PerSequencer[seqID]
	if !ok {
		return SequencerSyncInfo{}
	}
	latestSeen := info.latestBranchesSeen.Maximum(core.LessTxID)
	return SequencerSyncInfo{
		Synced:           info.latestBranchBooked.Timestamp() == latestSeen.Timestamp(),
		LatestBookedSlot: uint32(info.latestBranchBooked.TimeSlot()),
		LatestSeenSlot:   uint32(latestSeen.TimeSlot()),
	}
}

func (s *SyncData) allSequencersSynced() bool {
	for seqID := range s.PerSequencer {
		if !s.sequencerSyncInfo(seqID).Synced {
			return false
		}
	}
	return true
}

func syncWindowDuration() time.Duration {
	return core.TimeSlotDuration() * 2
}

// IsInSyncWindow returns true if latest added transaction (by timestamp) is no more than 1/2 time slot back from now
func (s *SyncData) isInSyncWindow() bool {
	return s.latestTransactionTSTime.Add(syncWindowDuration()).After(time.Now())
}

func (s *SyncData) IsSynced() bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.isInSyncWindow() && s.allSequencersSynced()
}
