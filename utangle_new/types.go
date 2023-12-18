package utangle_new

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/utangle_new/vertex"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

type (
	UTXOTangle struct {
		mutex      sync.RWMutex
		stateStore global.StateStore
		vertices   map[core.TransactionID]*vertex.WrappedTx
		branches   map[core.TimeSlot]map[*vertex.WrappedTx]common.VCommitment

		// all real-time related values in one place
		syncData *SyncData

		numAddedVertices   int
		numDeletedVertices int
		numAddedBranches   int
		numDeletedBranches int
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
