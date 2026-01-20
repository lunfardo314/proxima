package txstore

import (
	"errors"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
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
	txStoreHit                       prometheus.Counter
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

	s.txBytesCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txStore_txBytesCounter",
		Help: "new transaction bytes (cumulative size) counter in SimpleTxBytesStore",
	})

	s.txStoreHit = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txStore_hit",
		Help: "number of times transaction has been found in the store",
	})

	const lastSizeBucket = 2000

	s.txBytesSizeHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "proxima_txStore_txBytesSizeHistogram",
		Help:    "collects data about size of raw transaction bytes",
		Buckets: _makeBuckets(lastSizeBucket),
	})

	s.txBytesSeqNonBranchSizeHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "proxima_txStore_txBytesSeqNonBranchSizeHistogram",
		Help:    "collects data about size of raw sequencer non-branch transaction bytes",
		Buckets: _makeBuckets(lastSizeBucket),
	})
	reg.MustRegister(s.txCounter, s.txBytesCounter, s.txBytesSizeHistogram, s.txBytesSeqNonBranchSizeHistogram, s.txStoreHit)
}

func _makeBuckets(lastSize int) []float64 {
	ret := make([]float64, 0)
	for b := 0; b <= lastSize; b += 50 {
		ret = append(ret, float64(b))
	}
	return ret
}

func (s *SimpleTxBytesStore) PersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata, txidOpt ...base.TransactionID) (base.TransactionID, error) {
	var txid base.TransactionID
	var err error
	if len(txidOpt) > 0 {
		txid = txidOpt[0]
	} else {
		txid, err = transaction.IDFromParsedTransactionBytes(txBytes)
		if err != nil {
			return base.TransactionID{}, err
		}
	}
	if s.s.Has(txid[:]) {
		return txid, nil
	}

	//concat in the buffer and the dispose
	mdBytes := metadata.Bytes()

	txBytesWithMetadata := make([]byte, len(mdBytes)+len(txBytes))
	//txBytesWithMetadata := bytepool.GetArray(len(mdBytes) + len(txBytes))
	copy(txBytesWithMetadata, mdBytes)
	copy(txBytesWithMetadata[len(mdBytes):], txBytes)

	//s.s.Set(txid[:], common.ConcatBytes(metadata.Bytes(), txBytes))
	s.s.Set(txid[:], txBytesWithMetadata)

	if s.metricsEnabled {
		size := float64(len(txBytes))
		s.txCounter.Inc()
		s.txBytesCounter.Add(size)
		s.txBytesSizeHistogram.Observe(size)
		if txid.IsSequencerTransaction() && !txid.IsBranchTransaction() {
			s.txBytesSeqNonBranchSizeHistogram.Observe(size)
		}
	}

	//bytepool.DisposeArray(txBytesWithMetadata)
	return txid, nil
}

func (s *SimpleTxBytesStore) GetTxBytesWithMetadata(txid *base.TransactionID) []byte {
	ret := s.s.Get(txid[:])
	if s.metricsEnabled && ret != nil {
		s.txStoreHit.Inc()
	}
	return ret
}

func (s *SimpleTxBytesStore) HasTxBytes(txid *base.TransactionID) bool {
	return s.s.Has(txid[:])
}

func NewDummyTxBytesStore() DummyTxBytesStore {
	return DummyTxBytesStore{}
}

func (d DummyTxBytesStore) PersistTxBytesWithMetadata(_ []byte, _ *txmetadata.TransactionMetadata, _ ...base.TransactionID) (base.TransactionID, error) {
	return base.TransactionID{}, nil
}

func (d DummyTxBytesStore) GetTxBytesWithMetadata(_ *base.TransactionID) []byte {
	return nil
}

func (s DummyTxBytesStore) HasTxBytes(_ *base.TransactionID) bool {
	return false
}

func LoadAndParseTransaction(store global.TxBytesGet, txid base.TransactionID) (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	txBytesWithMetadata := store.GetTxBytesWithMetadata(&txid)
	if len(txBytesWithMetadata) == 0 {
		return nil, nil, errors.New("transaction not found")
	}
	txBytes, metadata, err := txmetadata.ParseTxMetadata(txBytesWithMetadata)
	if err != nil {
		return nil, nil, err
	}
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		return nil, nil, err
	}
	return tx, metadata, nil
}

func LoadOutput(store global.TxBytesGet, oid base.OutputID) (*ledger.Output, error) {
	tx, _, err := LoadAndParseTransaction(store, oid.TransactionID())
	if err != nil {
		return nil, err
	}
	return tx.ProducedOutputAt(oid.Index())
}
