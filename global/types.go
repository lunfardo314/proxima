package global

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

type (
	StoreReader interface {
		common.KVReader
		common.Traversable
		IsClosed() bool
	}

	Store interface {
		StoreReader
		common.BatchedUpdatable
	}
)

// transaction store interface definitions
type (
	TxBytesGet interface {
		// GetTxBytes returns raw transaction bytes (empty on absence).
		GetTxBytes(id *base.TransactionID) []byte
		HasTxBytes(txid *base.TransactionID) bool
	}
	TxBytesPersist interface {
		// PersistTxBytes saves raw transaction bytes. The transaction ID can
		// be supplied explicitly to avoid re-parsing; otherwise it is parsed
		// from the bytes.
		PersistTxBytes(txBytes []byte, txid ...base.TransactionID) (base.TransactionID, error)
		// PersistTxBytesBatch writes multiple transaction entries in a single DB transaction.
		// Implementation must not depend on map iteration order.
		PersistTxBytesBatch(batch map[base.TransactionID][]byte) error
	}
	TxBytesStore interface {
		TxBytesGet
		TxBytesPersist
	}
)

// transaction log interface

type (
	TxLogLevel int

	TxLogWriter interface {
		TxLog(timestamp time.Time, msg string, txid ...base.TransactionID)
	}

	TxLogRecord struct {
		ID             base.TransactionID
		ClockTimestamp time.Time
		Message        string
	}
	TxLogReader interface {
		// TxLogGet returns records sorted by timestamp in ascending order.
		TxLogGet(txShortIDPrefix []byte, max ...int) ([]TxLogRecord, error)
		TxLogIterate(begin time.Time, fun func(rec TxLogRecord)) error
	}

	TxLogger interface {
		TxLogEnable(lvl TxLogLevel)
	}
)

const (
	TxLogLevelOff TxLogLevel = iota
	TxLogLevelBranchTransactionsOnly
	TxLogLevelSequencerTransactionsOnly
	TxLogLevelNonSequencerTransactionsOnly
	TxLogLevelAllTransactions
)

type (
	Logging interface {
		Log() *zap.SugaredLogger
		Outputs() []string
		Tracef(tag string, format string, args ...any)
		TracefLog(log *zap.SugaredLogger, tag string, format string, args ...any)
		StartTracingTags(tags ...string)
		// Assertf asserts only if global shutdown wasn't issued
		Assertf(cond bool, format string, args ...any)
		AssertNoError(err error, prefix ...string)
		TopicVerbosityLevel(topic string) int
		LogTopicf(topic string, requiredLevel int, template string, args ...any)
		WarnTopicf(topic string, requiredLevel int, template string, args ...any)

		// counting

		IncCounter(name string)
		DecCounter(name string)
		SetCounter(name string, value int)
		Counter(name string) int
		CounterLines(prefix ...string) *lines.Lines
		AttachmentFinished(started time.Time, cost int)

		// TxPullParameters repeat after period, max attempts, num peers
		TxPullParameters() (repeatPeriod time.Duration, maxAttempts int)
		// DeadlockCatchingDisabled config parameter to disdable deadlock catching
		DeadlockCatchingDisabled() bool
		// LogTx log transaction
		LogTx(clockTs time.Time, msg string, txid ...base.TransactionID)
	}

	// StartStop interface of the global objects which coordinates graceful shutdown
	StartStop interface {
		Ctx() context.Context // global context of the node. Canceling means stopping the node
		Stop()
		// GracefulShutdown initiates orderly node shutdown with a reason logged prominently.
		// Callable from any context (detached vertex, signal handler, memory watchdog, etc.).
		// Idempotent — safe to call multiple times from different goroutines.
		GracefulShutdown(reason string)
		IsShuttingDown() bool
		ClockCatchUpWithLedgerTime(ts base.LedgerTime) bool
		MarkWorkProcessStarted(name string)
		MarkWorkProcessStopped(name string)
		RepeatInBackground(name string, period time.Duration, fun func() bool, skipFirst ...bool) // runs background goroutine
		RepeatSync(period time.Duration, fun func() bool) bool                                    // repeats synchronously. Returns false if was interrupted, true otherwise
		// IsSnapshotting returns true while a snapshot is being generated.
		// Used by attach queues and sequencer to shed load during snapshot.
		IsSnapshotting() bool
		SetSnapshotting(on bool)
		// MemoryPressureGC is a non-blocking nudge to the async GC worker. Safe on any hot path.
		// The worker serialises runtime.GC() off-thread, rate-limits to one GC per 5s, and only
		// runs when heap is above 50% of memory.limit_mb. No-op when limit not configured.
		MemoryPressureGC()
		MemLimitBytes() uint64
		// MemoryStressLevel returns the current memory stress level (0-100).
		// Computed as 100 * allocated / limit. Returns 0 when limit is not configured.
		MemoryStressLevel() int
	}

	Metrics interface {
		MetricsRegistry() *prometheus.Registry
	}

	NodeGlobal interface {
		Logging
		StartStop
		Metrics
		// ConsensusContribution returns this node's own consensus mass in tokens:
		// 0 if the node runs no sequencer, otherwise tokenBalance + frozenCoverage[0]
		// of the sequencer's own latest milestone chain output. Self-reported, used
		// only by the operational network-mapping overlay (see claude/network_connectivity.md).
		ConsensusContribution() uint64
	}

	Fraction struct {
		Numerator   int
		Denominator int
	}
)

var (
	FractionHalf = Fraction{
		Numerator:   1,
		Denominator: 2,
	}

	Fraction23 = Fraction{
		Numerator:   2,
		Denominator: 3,
	}

	ErrInterrupted = errors.New("interrupted by global stop")
)

// IsHealthyCoverageDelta returns true iff `coverageDelta > (num/den) * supply`,
// where the (num, den) values come from ledger constants (see
// `constHealthyCoverageNumerator` / `constHealthyCoverageDenominator`).
//
// The cross-multiplication form `coverageDelta * den > supply * num` is
// equivalent to the integer-division form for integer inputs, and is what the
// on-chain `healthyCoverageDelta` EasyFL function evaluates inside the
// stemLock constraint — so Go-side and on-chain checks agree bit-for-bit.
func IsHealthyCoverageDelta(coverageDelta, supply uint64, fraction Fraction) bool {
	return coverageDelta*uint64(fraction.Denominator) > supply*uint64(fraction.Numerator)
}

func (f Fraction) String() string {
	return fmt.Sprintf("%d/%d", f.Numerator, f.Denominator)
}
