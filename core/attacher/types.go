package attacher

import (
	"sync"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

type (
	DAGAccessEnvironment interface {
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *ledger.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.StateStore
		GetStateReaderForTheBranch(branch *ledger.TransactionID) global.IndexedStateReader
		GetStemWrappedOutput(branch *ledger.TransactionID) vertex.WrappedOutput
	}

	PullEnvironment interface {
		Pull(txid ledger.TransactionID)
		PokeMe(me, with *vertex.WrappedTx)
		PokeAllWith(wanted *vertex.WrappedTx)
	}

	PostEventEnvironment interface {
		PostEventNewGood(vid *vertex.WrappedTx)
		PostEventNewTransaction(vid *vertex.WrappedTx)
	}

	EvidenceEnvironment interface {
		EvidenceIncomingBranch(txid *ledger.TransactionID, seqID ledger.ChainID)
		EvidenceBookedBranch(txid *ledger.TransactionID, seqID ledger.ChainID)
	}

	Environment interface {
		global.Glb
		DAGAccessEnvironment
		PullEnvironment
		PostEventEnvironment
		EvidenceEnvironment
		AsyncPersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata)
		ParseMilestoneData(msVID *vertex.WrappedTx) *txbuilder.MilestoneData
	}

	attacher struct {
		Environment
		name     string
		err      error
		baseline *ledger.TransactionID
		vertices map[*vertex.WrappedTx]Flags
		rooted   map[*vertex.WrappedTx]set.Set[byte]
		pokeMe   func(vid *vertex.WrappedTx)
		coverage multistate.LedgerCoverage
		// only supported for branch transactions
		baselineSupply uint64
	}

	// IncrementalAttacher is used by the sequencer to build a sequencer milestone
	// transaction by adding new tag-along inputs one-by-one. It ensures the past cone is conflict-free
	// It is used to generate the transaction and after that it is discarded
	IncrementalAttacher struct {
		attacher
		endorse    []*vertex.WrappedTx
		inputs     []vertex.WrappedOutput
		targetTs   ledger.Time
		stemOutput vertex.WrappedOutput
	}

	// milestoneAttacher is used to attach a sequencer transaction
	milestoneAttacher struct {
		attacher
		vid       *vertex.WrappedTx
		metadata  *txmetadata.TransactionMetadata
		closeOnce sync.Once
		pokeChan  chan *vertex.WrappedTx
		pokeMutex sync.Mutex
		finals    *attachFinals
		closed    bool
	}

	_attacherOptions struct {
		metadata           *txmetadata.TransactionMetadata
		attachmentCallback func(vid *vertex.WrappedTx, err error)
		pullNonBranch      bool
		doNotLoadBranch    bool
		calledBy           string
	}
	Option func(*_attacherOptions)

	attachFinals struct {
		numInputs         int
		numOutputs        int
		coverage          multistate.LedgerCoverage
		slotInflation     uint64
		supply            uint64
		root              common.VCommitment
		numTransactions   int
		numCreatedOutputs int
		numDeletedOutputs int
		baseline          *ledger.TransactionID
	}

	Flags uint8

	SequencerCommandParser interface {
		// ParseSequencerCommandToOutput analyzes consumed output for sequencer command and produces
		// one or several outputs as an effect of the command. Returns:
		// - nil, nil if a syntactically valid sequencer command is not detected  in the inputs
		// - nil, err if a syntactically valid command can be detected, however it contains errors
		// - list of outputs, nil if it is a success
		ParseSequencerCommandToOutput(input *ledger.OutputWithID) ([]*ledger.Output, error)
	}
)

const (
	FlagAttachedVertexKnown             = 0b00000001
	FlagAttachedVertexDefined           = 0b00000010
	FlagAttachedVertexEndorsementsSolid = 0b00000100
	FlagAttachedVertexInputsSolid       = 0b00001000
)

func (f Flags) FlagsUp(fl Flags) bool {
	return f&fl == fl
}
