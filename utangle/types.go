package utangle

import (
	"sync"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/atomic"
)

type (
	UTXOTangle struct {
		mutex        sync.RWMutex
		stateStore   general.StateStore
		txBytesStore general.TxBytesStore
		vertices     map[core.TransactionID]*WrappedTx
		branches     map[core.TimeSlot]map[*WrappedTx]common.VCommitment

		lastPrunedOrphaned atomic.Time
		lastCutFinal       atomic.Time

		numAddedVertices   int
		numDeletedVertices int
		numAddedBranches   int
		numDeletedBranches int
	}

	pastTrack struct {
		forks    ForkSet
		branches []*WrappedTx
	}
	Vertex struct {
		Tx           *transaction.Transaction
		Inputs       []*WrappedTx
		Endorsements []*WrappedTx
		pastTrack    *pastTrack
		isSolid      bool
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

	ForkSet map[WrappedOutput]byte
)

const TipSlots = 5
