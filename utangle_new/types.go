package utangle_new

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

type (
	UTXOTangle struct {
		mutex      sync.RWMutex
		stateStore global.StateStore
		vertices   map[core.TransactionID]*WrappedTx
		branches   map[core.TimeSlot]map[*WrappedTx]common.VCommitment

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

	PastTrack struct {
		forks          *forkSet
		baselineBranch *WrappedTx
	}

	Vertex struct {
		Tx           *transaction.Transaction
		Inputs       []*WrappedTx
		Endorsements []*WrappedTx
		pastTrack    PastTrack
		isSolid      bool
		// during solidification, sometimes there's a need to look for output into all branches.
		// This flag prevent scanning all branches more than once
		branchesAlreadyScanned bool
	}

	VirtualTransaction struct {
		txid             core.TransactionID
		mutex            sync.RWMutex
		outputs          map[byte]*core.Output
		sequencerOutputs *[2]byte // if nil, it is unknown
	}

	Fork struct {
		ConflictSetID WrappedOutput
		SN            byte // max 256 double spends per output. Tx will be dropped if exceeded
	}

	forkSet struct {
		m     map[WrappedOutput]byte
		mutex sync.RWMutex
	}
)

const TipSlots = 5
