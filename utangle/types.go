package utangle

import (
	"sync"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util/set"
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
		Tx                 *transaction.Transaction
		StateDelta         UTXOStateDelta
		BranchConeTipSolid bool // to distinguish with the StateDelta.baselineBranch == nil
		Inputs             []*WrappedTx
		Endorsements       []*WrappedTx
	}

	UTXOStateDelta struct {
		baselineBranch *WrappedTx
		transactions   map[*WrappedTx]transactionData
		coverage       uint64
	}

	transactionData struct {
		consumed          set.Set[byte] // TODO optimize with bitmap?
		includedThisDelta bool
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
