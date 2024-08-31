package txstore

import (
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/unitrie/common"
	"github.com/prometheus/client_golang/prometheus"
)

type SimpleTxBytesStore struct {
	s                                common.KVStore
	metricsEnabled                   bool
	txCounter                        prometheus.Counter
	txBytesCounter                   prometheus.Counter
	txBytesSizeHistogram             prometheus.Histogram
	txBytesSeqNonBranchSizeHistogram prometheus.Histogram
}

type DummyTxBytesStore struct {
	s common.KVStore
}

func NewSimpleTxBytesStore(store common.KVStore, metricsRegistry ...global.Metrics) *SimpleTxBytesStore {
	ret := &SimpleTxBytesStore{s: store}
	if len(metricsRegistry) > 0 && metricsRegistry[0] != nil {
		ret.registerMetrics(metricsRegistry[0].MetricsRegistry())
	}
	return ret
}

func (s *SimpleTxBytesStore) registerMetrics(reg *prometheus.Registry) {
	s.metricsEnabled = true
	s.txCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txStore_txCounter",
		Help: "new transaction counter in SimpleTxBytesStore",
	})
	reg.MustRegister(s.txCounter)

	s.txBytesCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txStore_txBytesCounter",
		Help: "new transaction bytes (cumulative size) counter in SimpleTxBytesStore",
	})
	reg.MustRegister(s.txBytesCounter)

	const lastSizeBucket = 2000

	s.txBytesSizeHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "proxima_txStore_txBytesSizeHistogram",
		Help:    "collects data about size of raw transaction bytes",
		Buckets: _makeBuckets(lastSizeBucket),
	})
	reg.MustRegister(s.txBytesSizeHistogram)

	s.txBytesSeqNonBranchSizeHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "proxima_txStore_txBytesSeqNonBranchSizeHistogram",
		Help:    "collects data about size of raw sequencer non-branch transaction bytes",
		Buckets: _makeBuckets(lastSizeBucket),
	})
	reg.MustRegister(s.txBytesSeqNonBranchSizeHistogram)
}

func _makeBuckets(lastSize int) []float64 {
	ret := make([]float64, 0)
	for b := 0; b <= lastSize; b += 50 {
		ret = append(ret, float64(b))
	}
	return ret
}
func (s *SimpleTxBytesStore) PersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata) (ledger.TransactionID, error) {
	txid, err := transaction.IDFromTransactionBytes(txBytes)
	if err != nil {
		return ledger.TransactionID{}, err
	}
	if s.s.Has(txid[:]) {
		return txid, nil
	}
	if metadata != nil {
		mdTmp := *metadata
		mdTmp.IsResponseToPull = false // saving without the irrelevant metadata flag
		mdTmp.PortionInfo = nil
		metadata = &mdTmp
	}

	s.s.Set(txid[:], common.ConcatBytes(metadata.Bytes(), txBytes))

	if s.metricsEnabled {
		size := float64(len(txBytes))
		s.txCounter.Inc()
		s.txBytesCounter.Add(size)
		s.txBytesSizeHistogram.Observe(size)
		if txid.IsSequencerMilestone() && !txid.IsBranchTransaction() {
			s.txBytesSeqNonBranchSizeHistogram.Observe(size)
		}
	}
	return txid, nil
}

func (s *SimpleTxBytesStore) GetTxBytesWithMetadata(txid *ledger.TransactionID) []byte {
	return s.s.Get(txid[:])
}

func (s *SimpleTxBytesStore) HasTxBytes(txid *ledger.TransactionID) bool {
	return s.s.Has(txid[:])
}

func NewDummyTxBytesStore() DummyTxBytesStore {
	return DummyTxBytesStore{}
}

func (d DummyTxBytesStore) PersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata) (ledger.TransactionID, error) {
	return ledger.TransactionID{}, nil
}

func (d DummyTxBytesStore) GetTxBytesWithMetadata(_ *ledger.TransactionID) []byte {
	return nil
}

func (s DummyTxBytesStore) HasTxBytes(txid *ledger.TransactionID) bool {
	return false
}
