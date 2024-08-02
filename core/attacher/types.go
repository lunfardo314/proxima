package attacher

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

type (
	memDAGAccessEnvironment interface {
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *ledger.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.StateStore
		GetStateReaderForTheBranch(branch *ledger.TransactionID) global.IndexedStateReader
		GetStemWrappedOutput(branch *ledger.TransactionID) vertex.WrappedOutput
		SendToTippool(vid *vertex.WrappedTx)
		EvidenceBranchSlot(s ledger.Slot, healthy bool)
	}

	pullEnvironment interface {
		Pull(txid ledger.TransactionID)
		PokeMe(me, with *vertex.WrappedTx)
		PokeAllWith(wanted *vertex.WrappedTx)
		NotifyEndOfPortion()
	}

	postEventEnvironment interface {
		PostEventNewGood(vid *vertex.WrappedTx)
		PostEventNewTransaction(vid *vertex.WrappedTx)
	}

	Environment interface {
		global.NodeGlobal
		memDAGAccessEnvironment
		pullEnvironment
		postEventEnvironment
		AsyncPersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata)
		GossipAttachedTransaction(tx *transaction.Transaction, metadata *txmetadata.TransactionMetadata)
		ParseMilestoneData(msVID *vertex.WrappedTx) *ledger.MilestoneData
	}

	attacher struct {
		Environment
		name               string
		err                error
		baseline           *vertex.WrappedTx
		vertices           map[*vertex.WrappedTx]Flags
		rooted             map[*vertex.WrappedTx]set.Set[byte]
		referenced         set.Set[*vertex.WrappedTx]
		pokeMe             func(vid *vertex.WrappedTx)
		coverage           uint64
		coverageAdjustment uint64
		coverageAdjusted   bool
		slotInflation      uint64
		// only supported for branch transactions
		baselineSupply uint64
		// trace this local attacher with all tags
		forceTrace string
		// for incremental attacher we need slightly extended conflict checker
		checkConflictsFunc func(consumerVertex *vertex.Vertex, consumerTx *vertex.WrappedTx) checkConflictingConsumersFunc
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
		vid                  *vertex.WrappedTx
		metadata             *txmetadata.TransactionMetadata
		timeoutContext       context.Context
		cancelTimeoutContext context.CancelFunc
		closeOnce            sync.Once
		pokeChan             chan struct{}
		pokeClosingMutex     sync.RWMutex
		finals               attachFinals
		closed               bool
	}

	_attacherOptions struct {
		metadata           *txmetadata.TransactionMetadata
		attachmentCallback func(vid *vertex.WrappedTx, err error)
		pullNonBranch      bool
		doNotLoadBranch    bool
		calledBy           string
		enforceTimestamp   bool
		timeout            time.Duration
	}
	AttachTxOption func(*_attacherOptions)

	// final values of attacher run. Ugly -> TODO refactor
	attachFinals struct {
		numInputs          int
		numOutputs         int
		coverage           uint64
		slotInflation      uint64
		supply             uint64
		root               common.VCommitment
		baseline           *ledger.TransactionID
		numVertices        int
		numNewTransactions uint32
		numCreatedOutputs  int
		numDeletedOutputs  int
		started            time.Time
		numMissedPokes     atomic.Int32
		numPokes           int
		numPeriodic        int
		numRooted          int
	}

	Flags uint8

	checkConflictingConsumersFunc func(existingConsumers set.Set[*vertex.WrappedTx]) (conflict *vertex.WrappedTx)

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
	FlagAttachedVertexAskedForPoke      = 0b00010000
)

func (f Flags) FlagsUp(fl Flags) bool {
	return f&fl == fl
}

func (f Flags) String() string {
	return fmt.Sprintf("%08b known = %v, defined = %v, endorsementsOk = %v, inputsOk = %v, asked for poke = %v",
		f,
		f.FlagsUp(FlagAttachedVertexKnown),
		f.FlagsUp(FlagAttachedVertexDefined),
		f.FlagsUp(FlagAttachedVertexEndorsementsSolid),
		f.FlagsUp(FlagAttachedVertexInputsSolid),
		f.FlagsUp(FlagAttachedVertexAskedForPoke),
	)
}

func AttachTxOptionWithTransactionMetadata(metadata *txmetadata.TransactionMetadata) AttachTxOption {
	return func(options *_attacherOptions) {
		options.metadata = metadata
	}
}

func AttachTxOptionWithAttachmentCallback(fun func(vid *vertex.WrappedTx, err error)) AttachTxOption {
	return func(options *_attacherOptions) {
		options.attachmentCallback = fun
	}
}

// AttachTxOptionWithTimeout default is infinite
func AttachTxOptionWithTimeout(t time.Duration) AttachTxOption {
	return func(options *_attacherOptions) {
		options.timeout = t
	}
}

func OptionPullNonBranch(options *_attacherOptions) {
	options.pullNonBranch = true
}

func OptionDoNotLoadBranch(options *_attacherOptions) {
	options.doNotLoadBranch = true
}

func OptionEnforceTimestampBeforeRealTime(options *_attacherOptions) {
	options.enforceTimestamp = true
}

func OptionInvokedBy(name string) AttachTxOption {
	return func(options *_attacherOptions) {
		options.calledBy = name
	}
}
