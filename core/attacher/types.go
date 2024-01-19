package attacher

import (
	"context"
	"sync"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

type (
	DAGAccessEnvironment interface {
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *ledger.TransactionID) *vertex.WrappedTx
		GetVertex(txid *ledger.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.StateStore
		GetStateReaderForTheBranch(branch *vertex.WrappedTx) global.IndexedStateReader
		AddBranchNoLock(branch *vertex.WrappedTx)
	}

	PullEnvironment interface {
		Pull(txid ledger.TransactionID)
		PokeMe(me, with *vertex.WrappedTx)
		PokeAllWith(wanted *vertex.WrappedTx)
	}

	PostEventEnvironment interface {
		PostEventNewGood(vid *vertex.WrappedTx)
		PostEventNewValidated(vid *vertex.WrappedTx)
	}

	EvidenceEnvironment interface {
		EvidenceIncomingBranch(txid *ledger.TransactionID, seqID ledger.ChainID)
		EvidenceBookedBranch(txid *ledger.TransactionID, seqID ledger.ChainID)
	}

	Environment interface {
		global.Logging
		DAGAccessEnvironment
		PullEnvironment
		PostEventEnvironment
		EvidenceEnvironment
		AsyncPersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata)
	}

	attacher struct {
		Environment
		name                  string
		reason                error
		baselineBranch        *vertex.WrappedTx
		validPastVertices     set.Set[*vertex.WrappedTx]
		undefinedPastVertices set.Set[*vertex.WrappedTx]
		rooted                map[*vertex.WrappedTx]set.Set[byte]
		pokeMe                func(vid *vertex.WrappedTx)
		forceTrace1Ahead      bool
		prevCoverage          multistate.LedgerCoverage // set when baseline is determined
		coverageDelta         uint64
	}

	// IncrementalAttacher is used by the sequencer to build a sequencer milestone
	// transaction by adding new tag-along inputs one-by-one. It ensures the past cone is conflict-free
	// It is used to generate the transaction and after that it is discarded
	IncrementalAttacher struct {
		attacher
		extend         *vertex.WrappedTx
		endorse        []*vertex.WrappedTx
		tagAlongInputs []vertex.WrappedOutput
		targetTs       ledger.LogicalTime
		seqOutput      vertex.WrappedOutput
		stemOutput     vertex.WrappedOutput
	}

	// milestoneAttacher is used to attach a sequencer transaction
	milestoneAttacher struct {
		attacher
		vid       *vertex.WrappedTx
		metadata  *txmetadata.TransactionMetadata
		ctx       context.Context
		closeOnce sync.Once
		pokeChan  chan *vertex.WrappedTx
		pokeMutex sync.Mutex
		finals    *attachFinals
		closed    bool
	}

	_attacherOptions struct {
		ctx                context.Context
		metadata           *txmetadata.TransactionMetadata
		attachmentCallback func(vid *vertex.WrappedTx, err error)
		pullNonBranch      bool
		doNotLoadBranch    bool
		calledBy           string
	}
	Option func(*_attacherOptions)

	attachFinals struct {
		coverage          multistate.LedgerCoverage
		root              common.VCommitment
		numTransactions   int
		numCreatedOutputs int
		numDeletedOutputs int
		baseline          *vertex.WrappedTx
	}
)
