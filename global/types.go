package global

import (
	"context"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/unitrie/common"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

type (
	StateReader interface {
		GetUTXO(id *ledger.OutputID) ([]byte, bool)
		HasUTXO(id *ledger.OutputID) bool
		KnowsCommittedTransaction(txid *ledger.TransactionID) bool // all txids are kept in the state for some time
	}

	StateIndexReader interface {
		GetIDsLockedInAccount(addr ledger.AccountID) ([]ledger.OutputID, error)
		GetUTXOsLockedInAccount(accountID ledger.AccountID) ([]*ledger.OutputDataWithID, error)
		GetUTXOForChainID(id *ledger.ChainID) (*ledger.OutputDataWithID, error)
		Root() common.VCommitment
		MustLedgerIdentityBytes() []byte // either state identity consistent or panic
	}

	// IndexedStateReader state and indexer readers packing together
	IndexedStateReader interface {
		StateReader
		StateIndexReader
	}

	StateStoreReader interface {
		common.KVReader
		common.Traversable
		IsClosed() bool
	}

	StateStore interface {
		StateStoreReader
		common.BatchedUpdatable
	}

	TxBytesGet interface {
		// GetTxBytesWithMetadata return empty slice on absence, otherwise returns concatenated metadata bytes and transaction bytes
		GetTxBytesWithMetadata(id *ledger.TransactionID) []byte
		HasTxBytes(txid *ledger.TransactionID) bool
	}

	TxBytesPersist interface {
		// PersistTxBytesWithMetadata saves txBytes prefixed with metadata bytes.
		// metadata == nil is interpreted as empty metadata (one 0 byte as prefix)
		PersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata) (ledger.TransactionID, error)
	}

	TxBytesStore interface {
		TxBytesGet
		TxBytesPersist
	}

	Logging interface {
		Log() *zap.SugaredLogger
		Tracef(tag string, format string, args ...any)
		TraceTx(txid *ledger.TransactionID, format string, args ...any)
		Assertf(cond bool, format string, args ...any)
		AssertNoError(err error, prefix ...string)
		AssertMustError(err error)
		LogAttacherStats() bool
	}

	// StartStop interface of the global objet\ct which coordinates graceful shutdown
	StartStop interface {
		Ctx() context.Context
		Stop()
		MarkWorkProcessStarted(name string)
		MarkWorkProcessStopped(name string)
		RepeatEvery(period time.Duration, fun func() bool, skipFirst ...bool) // runs background goroutine
	}

	TraceTx interface {
		StartTracingTx(txid ledger.TransactionID)
		StopTracingTx(txid ledger.TransactionID)
	}

	Metrics interface {
		MetricsRegistry() *prometheus.Registry
	}

	NodeGlobal interface {
		Logging
		TraceTx
		StartStop
		Metrics
	}
)
