package utangle

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/unitrie/common"
)

type (
	UTXOTangle struct {
		mutex        sync.RWMutex
		stateStore   global.StateStore
		txBytesStore global.TxBytesStore
		vertices     map[core.TransactionID]*WrappedTx
		branches     map[core.TimeSlot]map[*WrappedTx]common.VCommitment

		// all real-time related values in one place
		syncStatus *SyncStatus

		numAddedVertices   int
		numDeletedVertices int
		numAddedBranches   int
		numDeletedBranches int
	}

	SyncStatus struct {
		mutex sync.RWMutex
		// when node whenStarted
		whenStarted time.Time
		// latestTransactionTSTime time converted from latest attached transaction timestamp.
		// Is used  to determine synced of not
		latestTransactionTSTime time.Time
		// last time pruner was run
		lastPrunedOrphaned time.Time
		// last time final cuter was run
		lastCutFinal time.Time
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
