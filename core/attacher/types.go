package attacher

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	memDAGAccessEnvironment interface {
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid base.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.Store
		GetStemWrappedOutput(branch base.TransactionID) vertex.WrappedOutput
		SendToTippool(vid *vertex.WrappedTx)
		EvidenceBranchSlot(s uint32, healthy bool)
		// RegisterBranchVertices records the vertex set of a branch's past cone for fine-grained pruning.
		RegisterBranchVertices(branchID base.TransactionID, predecessorBranchID base.TransactionID, vertices set.Set[*vertex.WrappedTx])
		TxBytesStore() global.TxBytesStore
		// GetTxBytes checks the transaction cache first, then the store.
		GetTxBytes(txid *base.TransactionID) []byte
		// TakeCachedTx returns a pre-parsed transaction from the cache and removes it.
		// Returns nil if not cached.
		TakeCachedTx(txid *base.TransactionID) *transaction.Transaction
		// CachedTxInSolicited sends a pre-parsed transaction to the solicit queue (fast-track, no rate control).
		CachedTxInSolicited(tx *transaction.Transaction)
		// TxBytesFromStoreInSolicited sends raw txstore bytes to the solicit queue (fallback for disk-only lookups).
		TxBytesFromStoreInSolicited(txBytes []byte)
		AddPulledTransaction(txid base.TransactionID)
	}

	pullEnvironment interface {
		PokeMe(me, with *vertex.WrappedTx)
		PokeAllWith(wanted *vertex.WrappedTx)
		PullFromPeers(txid base.TransactionID) int
	}

	postEventEnvironment interface {
		PostEventNewTransaction(vid *vertex.WrappedTx)
		PostEventNewVertex(tx *transaction.Transaction, seqName string)
	}

	Environment interface {
		global.NodeGlobal
		memDAGAccessEnvironment
		pullEnvironment
		postEventEnvironment
		ParseMilestoneData(msVID *vertex.WrappedTx) *seqdata.SequencerData
		EvidencePastConeSize(sz int)
		EvidenceBranchMutations(numMutations int)
		DurationSinceLastMessageFromPeer() time.Duration
		IsConnectedToNetwork() bool
		Branches() *branches.Branches
		EvidenceTxValidationStats(took time.Duration, numIn, numOut int)
		EvidenceBranchInflationBonus(ib uint64)
		// LatestForwardSyncedTimestamp returns the timestamp of the current forward-sync target.
		// Attachers with dependencies at or before this timestamp skip the depth cap.
		// Returns zero LedgerTime when forward-sync is idle.
		LatestForwardSyncedTimestamp() base.LedgerTime
		// SuppressHealthEnforcement, when true, makes the attacher accept unhealthy
		// branch transactions (node-global 'suppress_health_enforcement' flag).
		SuppressHealthEnforcement() bool
	}

	attacher struct {
		Environment
		*ledger.Library // cached library for transaction slot
		pastCone        *vertex.PastCone
		name            string
		err             error
		closed          bool
		pokeMe          func(vid *vertex.WrappedTx)
		// seqTxCost is the attachment cost of the sequencer transaction being attached.
		// For milestone attacher: set to tip's cost (numInputs + numOutputs).
		// For incremental attacher: set to 0 (budget check happens in atomicCheck callback instead).
		seqTxCost int
		// getBaselineStateReader returns a StateReader for a branch.
		// For milestoneAttacher: defaults to GetStateReaderForTheBranch (triggers lazy DB commit).
		// For IncrementalAttacher: set to GetVirtualStateReaderForTheBranch (no DB commit).
		getBaselineStateReader func(base.TransactionID) multistate.StateReader
		// onDetachedVertex is called when the attacher encounters a DetachedVertex during past cone traversal.
		// milestoneAttacher: triggers reattachment via go AttachTransaction(tx, env).
		// IncrementalAttacher: nil — returns error, sequencer abandons the proposal.
		onDetachedVertex func(vid *vertex.WrappedTx, tx *transaction.Transaction)
	}

	// IncrementalAttacher the sequencer uses it to build a sequencer milestone
	// transaction by adding new tag-along inputs one-by-one. It ensures the past cone is conflict-free
	// It is used to generate the transaction and after that it is discarded.
	// The attacher is agnostic about the exact target timestamp — it only needs the target slot
	// and whether the target is a branch transaction. The exact timestamp is determined later
	// via TimestampLowerBound() when building the final transaction.
	IncrementalAttacher struct {
		attacher
		endorse            []*vertex.WrappedTx
		inputs             []vertex.WrappedOutput
		targetSlot         uint32
		isBranch           bool
		stemOutput         vertex.WrappedOutput
		explicitBaselineID *base.TransactionID
		inflationAmount    uint64
	}

	// milestoneAttacher is used to attach a sequencer transaction
	milestoneAttacher struct {
		attacher
		vid              *vertex.WrappedTx
		providedMetadata *txmetadata.TransactionMetadata
		ctx              context.Context // override global one if not nil
		closeOnce        sync.Once
		pokeChan         chan struct{}
		pokeClosingMutex sync.RWMutex
		finals           attachFinals
		closed           bool
	}

	_attacherOptions struct {
		metadata           *txmetadata.TransactionMetadata
		attachmentCallback func(vid *vertex.WrappedTx, err error)
		calledBy           string
		enforceTimestamp   bool
		ctx                context.Context
		depth              int
	}
	AttachTxOption func(*_attacherOptions)

	// final values of attacher run.
	attachFinals struct {
		started     time.Time
		numInputs   int
		numOutputs  int
		numVertices int
		baseline    base.TransactionID
		// Locally-computed aggregates, always populated by wrapUpAttacher.
		// Used to live on TransactionMetadata as pointers signalling optional
		// presence; here they are always present (metadata-refactor §7).
		CoverageDelta  uint64
		LedgerCoverage uint64
		SlotInflation  uint64
		Supply         uint64
		vertex.MutationStats
	}

	SequencerCommandParser interface {
		// ParseSequencerCommandToOutputs analyzes consumed output for sequencer command and produces
		// one or several outputs as an effect of the command. Returns:
		// - nil, nil if a syntactically valid sequencer command is not detected  in the inputs
		// - nil, err if a syntactically valid command can be detected, however it contains errors
		// - list of outputs, nil if it is a success
		ParseSequencerCommandToOutputs(input *ledger.OutputWithID) ([]*ledger.Output, error)
	}
)

var ErrSolidificationDeadline = errors.New("solidification deadline")

// ErrAttacherTransientStaleState signals that an attacher detected a dependency
// that has been reset (reattached) underneath it during its run — typically
// observed in the wrap-up consistency checks where an input's ledger coverage
// has been cleared by ReattachVertexNoLock before this attacher could read it.
// The consumer transaction itself is fine; the attacher just can't trust its
// own past-cone snapshot anymore. Callers must NOT mark the consumer Bad —
// the attempt is abandoned and the framework retries.
var ErrAttacherTransientStaleState = errors.New("attacher transient stale state")

func WithTransactionMetadata(metadata *txmetadata.TransactionMetadata) AttachTxOption {
	return func(options *_attacherOptions) {
		options.metadata = metadata
	}
}

func WithAttachmentCallback(fun func(vid *vertex.WrappedTx, err error)) AttachTxOption {
	return func(options *_attacherOptions) {
		options.attachmentCallback = fun
	}
}

func WithEnforceTimestampBeforeRealTime(options *_attacherOptions) {
	options.enforceTimestamp = true
}

func WithInvokedBy(name string) AttachTxOption {
	return func(options *_attacherOptions) {
		options.calledBy = name
	}
}

func WithAttachmentDepth(depth int) AttachTxOption {
	return func(options *_attacherOptions) {
		options.depth = depth
	}
}
