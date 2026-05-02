package txstore

import (
	"errors"

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

// PersistTxBytes persists raw transaction bytes (see metadata-refactor §7 —
// persistent metadata is gone, the trie-committed stem carries the deterministic
// aggregates).
func (s *SimpleTxBytesStore) PersistTxBytes(txBytes []byte, txidOpt ...base.TransactionID) (base.TransactionID, error) {
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

	s.s.Set(txid[:], txBytes)

	if s.metricsEnabled {
		size := float64(len(txBytes))
		s.txCounter.Inc()
		s.txBytesCounter.Add(size)
		s.txBytesSizeHistogram.Observe(size)
		if txid.IsSequencerTransaction() && !txid.IsBranchTransaction() {
			s.txBytesSeqNonBranchSizeHistogram.Observe(size)
		}
	}
	return txid, nil
}

func (s *SimpleTxBytesStore) GetTxBytes(txid *base.TransactionID) []byte {
	ret := s.s.Get(txid[:])
	if s.metricsEnabled && ret != nil {
		s.txStoreHit.Inc()
	}
	return ret
}

func (s *SimpleTxBytesStore) HasTxBytes(txid *base.TransactionID) bool {
	return s.s.Has(txid[:])
}

// Iterator exposes prefix iteration over the underlying KV store. Used by the
// DAG explorer to walk all txids belonging to a given slot (5-byte timestamp
// prefix). Panics if the backing store doesn't implement common.Traversable;
// both production backends (BadgerDB and InMemoryKVStore) do.
func (s *SimpleTxBytesStore) Iterator(prefix []byte) common.KVIterator {
	return s.s.(common.Traversable).Iterator(prefix)
}

// PersistTxBytesBatch writes multiple entries in a single DB transaction.
// Uses BatchedWriter if available, otherwise falls back to individual writes.
func (s *SimpleTxBytesStore) PersistTxBytesBatch(batch map[base.TransactionID][]byte) error {
	if len(batch) == 0 {
		return nil
	}
	if batchable, ok := s.s.(common.BatchedUpdatable); ok {
		w := batchable.BatchedWriter()
		for txid, data := range batch {
			key := txid
			w.Set(key[:], data)
		}
		return w.Commit()
	}
	// fallback: individual writes
	for txid, data := range batch {
		key := txid
		s.s.Set(key[:], data)
	}
	return nil
}

func NewDummyTxBytesStore() DummyTxBytesStore {
	return DummyTxBytesStore{}
}

func (d DummyTxBytesStore) PersistTxBytes(_ []byte, _ ...base.TransactionID) (base.TransactionID, error) {
	return base.TransactionID{}, nil
}

func (d DummyTxBytesStore) PersistTxBytesBatch(_ map[base.TransactionID][]byte) error {
	return nil
}

func (d DummyTxBytesStore) GetTxBytes(_ *base.TransactionID) []byte {
	return nil
}

func (s DummyTxBytesStore) HasTxBytes(_ *base.TransactionID) bool {
	return false
}

// LoadAndParseTransaction loads raw transaction bytes from the store and parses them.
func LoadAndParseTransaction(store global.TxBytesGet, txid base.TransactionID) (*transaction.Transaction, error) {
	txBytes := store.GetTxBytes(&txid)
	if len(txBytes) == 0 {
		return nil, errors.New("transaction not found")
	}
	return transaction.Parse(txBytes)
}

func LoadOutput(store global.TxBytesGet, oid base.OutputID) (*ledger.Output, error) {
	tx, err := LoadAndParseTransaction(store, oid.TransactionID())
	if err != nil {
		return nil, err
	}
	return tx.ProducedOutputAt(oid.Index())
}
