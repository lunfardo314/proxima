package dag

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	DAG struct {
		mutex      sync.RWMutex
		stateStore global.StateStore
		vertices   map[core.TransactionID]*vertex.WrappedTx
		branches   map[*vertex.WrappedTx]global.IndexedStateReader
		// all real-time related values in one place
		syncData *SyncData
	}

	// SyncData contains various sync-related values. Thread safe with getters ad setters
	SyncData struct {
		mutex sync.RWMutex
		// latestTransactionTSTime time converted from latest attached transaction timestamp.
		// Is used  to determine synced of not
		latestTransactionTSTime time.Time
		// last time pruner was run
		lastPrunedOrphaned time.Time
		// last time final cuter was run
		lastCutFinal time.Time
		StartTime    time.Time
		PerSequencer map[core.ChainID]SequencerSyncStatus
	}

	SyncInfo struct {
		Synced       bool
		InSyncWindow bool
		PerSequencer map[core.ChainID]SequencerSyncInfo
	}

	SequencerSyncInfo struct {
		Synced           bool
		LatestBookedSlot uint32
		LatestSeenSlot   uint32
	}

	SequencerSyncStatus struct {
		latestBranchesSeen set.Set[core.TransactionID]
		latestBranchBooked core.TransactionID
	}
)

const TipSlots = 5

func New(stateStore global.StateStore) *DAG {
	return &DAG{
		stateStore: stateStore,
		vertices:   make(map[core.TransactionID]*vertex.WrappedTx),
		branches:   make(map[*vertex.WrappedTx]global.IndexedStateReader),
		syncData:   newSyncData(),
	}
}

type testingPostEventsEnvironment struct{}
