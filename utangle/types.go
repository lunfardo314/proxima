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

	Vertex struct {
		Tx           *transaction.Transaction
		StateDelta   UTXOStateDelta2
		Inputs       []*WrappedTx
		Endorsements []*WrappedTx
	}

	VirtualTransaction struct {
		txid             core.TransactionID
		mutex            sync.RWMutex
		outputs          map[byte]*core.Output
		sequencerOutputs *[2]byte // if nil, it is unknown
	}
)

const (
	TipSlots            = 5
	LedgerCoverageSlots = 2
)
