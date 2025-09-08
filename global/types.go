package global

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

type (
	TxBytesGet interface {
		// GetTxBytesWithMetadata return empty slice on absence, otherwise returns concatenated metadata bytes and transaction bytes
		GetTxBytesWithMetadata(id *base.TransactionID) []byte
		HasTxBytes(txid *base.TransactionID) bool
	}

	TxBytesPersist interface {
		// PersistTxBytesWithMetadata saves txBytes prefixed with metadata bytes.
		// metadata == nil is interpreted as empty metadata (one 0 byte as prefix)
		// optionally, transaction ChainID can be provided to avoid the need to parse the transaction bytes. In the latter case txid is used as DB key as is
		PersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata, txid ...base.TransactionID) (base.TransactionID, error)
	}

	TxBytesStore interface {
		TxBytesGet
		TxBytesPersist
	}

	Logging interface {
		Log() *zap.SugaredLogger
		Outputs() []string
		Tracef(tag string, format string, args ...any)
		StartTracingTags(tags ...string)
		// Assertf asserts only if global shutdown wasn't issued
		Assertf(cond bool, format string, args ...any)
		AssertNoError(err error, prefix ...string)
		VerbosityLevel() int
		Infof0(template string, args ...any)
		Infof1(template string, args ...any)
		Infof2(template string, args ...any)

		// counting

		IncCounter(name string)
		DecCounter(nane string)
		Counter(name string) int
		CounterLines(prefix ...string) *lines.Lines
		AttachmentFinished(started ...time.Time)

		// TxPullParameters repeat after period, max attempts, num peers
		TxPullParameters() (repeatPeriod time.Duration, maxAttempts int, numPeers int)
		// DeadlockCatchingDisabled config parameter to disdable deadlock catching
		DeadlockCatchingDisabled() bool
	}

	// StartStop interface of the global objects which coordinates graceful shutdown
	StartStop interface {
		Ctx() context.Context // global context of the node. Canceling means stopping the node
		Stop()
		IsShuttingDown() bool
		ClockCatchUpWithLedgerTime(ts base.LedgerTime)
		MarkWorkProcessStarted(name string)
		MarkWorkProcessStopped(name string)
		RepeatInBackground(name string, period time.Duration, fun func() bool, skipFirst ...bool) // runs background goroutine
		RepeatSync(period time.Duration, fun func() bool) bool                                    // repeats synchronously. Returns false if was interrupted, true otherwise
	}

	Metrics interface {
		MetricsRegistry() *prometheus.Registry
	}

	NodeGlobal interface {
		Logging
		StartStop
		Metrics
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

	FractionHealthyBranch = FractionHalf

	ErrInterrupted = errors.New("interrupted by global stop")
)

// IsHealthyCoverageDelta coverage delta is healthy if it is bigger than the fraction of supply
func IsHealthyCoverageDelta(coverageDelta, supply uint64, fraction Fraction) bool {
	return coverageDelta > (uint64(fraction.Numerator)*supply)/uint64(fraction.Denominator)
}

func (f *Fraction) String() string {
	return fmt.Sprintf("%d/%d", f.Numerator, f.Denominator)
}
