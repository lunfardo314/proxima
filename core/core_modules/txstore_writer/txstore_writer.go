// Package txstore_writer implements batched asynchronous writes to the transaction store
// and a cache of recently seen pre-parsed transactions.
//
// Write buffer: collects transactions and flushes them as a single batch to BadgerDB.
// This reduces write amplification and compaction pressure under high TPS.
//
// Transaction cache: keeps up to maxCacheSize recently seen *Transaction objects.
// When a dependency is needed during attachment (pull cycle), the cache serves
// the pre-parsed transaction directly, avoiding redundant parsing.
// Pulled transactions are removed from the cache (but not from the write buffer)
// because once attached to the memDAG, other attachers find them there.
// Capacity-based eviction removes oldest entries.
package txstore_writer

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

const (
	Name          = "txStoreWriter"
	maxBatchSize  = 100
	maxFlushDelay = 500 * time.Millisecond
	maxCacheSize  = 1000
)

type (
	environment interface {
		global.NodeGlobal
	}

	cachedTx struct {
		*transaction.Transaction
		metadata *txmetadata.TransactionMetadata
	}

	TxStoreWriter struct {
		environment
		store global.TxBytesStore

		mu    sync.Mutex
		cache map[base.TransactionID]cachedTx // value type, not pointer — fewer heap references
		// write buffer: data needed to flush to DB, independent of cache.
		// An entry can be removed from cache (on pull) but must remain
		// in writeBuf until flushed.
		writeBuf []writeBufEntry
		timer    *time.Timer
		// FIFO order for capacity eviction
		evictOrder []base.TransactionID
	}

	writeBufEntry struct {
		txid     base.TransactionID
		tx       *transaction.Transaction
		metadata *txmetadata.TransactionMetadata
	}
)

func New(env environment, store global.TxBytesStore) *TxStoreWriter {
	ret := &TxStoreWriter{
		environment: env,
		store:       store,
		cache:       make(map[base.TransactionID]cachedTx, maxCacheSize),
		writeBuf:    make([]writeBufEntry, 0, maxBatchSize),
		evictOrder:  make([]base.TransactionID, 0, maxCacheSize),
	}

	env.MarkWorkProcessStarted(Name)
	go func() {
		<-env.Ctx().Done()
		ret.flush()
		env.MarkWorkProcessStopped(Name)
		env.Log().Infof("[%s] STOPPED", Name)
	}()

	env.Log().Infof("[%s] STARTED (batch size: %d, flush delay: %v, cache size: %d)",
		Name, maxBatchSize, maxFlushDelay, maxCacheSize)
	return ret
}

// PersistTxBytesQueued adds a pre-parsed transaction to the cache and write buffer.
// The actual DB write happens when the buffer is full or the flush timer fires.
func (w *TxStoreWriter) PersistTxBytesQueued(tx *transaction.Transaction, metadata *txmetadata.TransactionMetadata) {
	if metadata == nil {
		metadata = &txmetadata.TransactionMetadata{}
	}
	txid := tx.ID()

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.cache[txid]; exists {
		return // already cached
	}
	if w.store.HasTxBytes(&txid) {
		return // already persisted
	}

	w.evictIfNeededLocked()

	w.cache[txid] = cachedTx{
		Transaction: tx,
		metadata:    metadata,
	}
	w.evictOrder = append(w.evictOrder, txid)
	w.writeBuf = append(w.writeBuf, writeBufEntry{
		txid:     txid,
		tx:       tx,
		metadata: metadata,
	})

	if len(w.writeBuf) >= maxBatchSize {
		w.flushLocked()
		return
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(maxFlushDelay, func() {
			w.flush()
		})
	}
}

// GetCachedTx returns the cached transaction and metadata, or nil if not in cache.
// Does NOT remove the entry from cache.
func (w *TxStoreWriter) GetCachedTx(txid *base.TransactionID) (*transaction.Transaction, *txmetadata.TransactionMetadata) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ct, exists := w.cache[*txid]
	if !exists {
		return nil, nil
	}
	return ct.Transaction, ct.metadata
}

// TakeCachedTx returns the cached transaction and metadata, and removes the entry
// from the cache. The write buffer is not affected — the transaction will still be
// flushed to DB. Use this when the transaction is about to be attached to the memDAG.
func (w *TxStoreWriter) TakeCachedTx(txid *base.TransactionID) (*transaction.Transaction, *txmetadata.TransactionMetadata) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ct, exists := w.cache[*txid]
	if !exists {
		return nil, nil
	}
	delete(w.cache, *txid)
	return ct.Transaction, ct.metadata
}

// HasCached checks if a transaction is in the cache.
func (w *TxStoreWriter) HasCached(txid *base.TransactionID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, exists := w.cache[*txid]
	return exists
}

// GetTxBytesWithMetadata returns combined metadata+txBytes for a cached transaction,
// or nil if not in cache. Used by pull_tx_server which needs raw bytes to send to peers.
func (w *TxStoreWriter) GetTxBytesWithMetadata(txid *base.TransactionID) []byte {
	w.mu.Lock()
	ct, exists := w.cache[*txid]
	w.mu.Unlock()

	if !exists {
		return nil
	}
	return combineTxBytesWithMetadata(ct.Bytes(), ct.metadata)
}

// CacheSize returns the number of items currently in the cache.
func (w *TxStoreWriter) CacheSize() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.cache)
}

// PendingCount returns the number of items in the write buffer (not yet flushed to DB).
func (w *TxStoreWriter) PendingCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writeBuf)
}

// flush acquires the lock and flushes.
func (w *TxStoreWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

// flushLocked writes all pending items to the store as a batch. Caller must hold w.mu.
func (w *TxStoreWriter) flushLocked() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if len(w.writeBuf) == 0 {
		return
	}

	batch := make(map[base.TransactionID][]byte, len(w.writeBuf))
	for _, entry := range w.writeBuf {
		batch[entry.txid] = combineTxBytesWithMetadata(entry.tx.Bytes(), entry.metadata)
	}
	w.writeBuf = w.writeBuf[:0]

	if err := w.store.PersistTxBytesBatch(batch); err != nil {
		w.Log().Errorf("[%s] batch write failed (%d items): %v", Name, len(batch), err)
		for txid, data := range batch {
			key := txid
			_, err2 := w.store.PersistTxBytesWithMetadata(data, nil, key)
			if err2 != nil {
				w.Log().Errorf("[%s] individual write also failed for %s: %v", Name, key.StringShort(), err2)
			}
		}
		return
	}
	w.SetCounter("txstore_batch", len(batch))
}

// evictIfNeededLocked evicts oldest entries when cache is at capacity.
// Caller must hold w.mu.
func (w *TxStoreWriter) evictIfNeededLocked() {
	if len(w.cache) < maxCacheSize {
		return
	}
	// evict ~10% to avoid frequent eviction
	target := maxCacheSize / 10
	if target < 1 {
		target = 1
	}
	newStart := 0
	evicted := 0
	for i, txid := range w.evictOrder {
		if evicted >= target {
			newStart = i
			break
		}
		if _, exists := w.cache[txid]; !exists {
			// already removed (via TakeCachedTx)
			continue
		}
		delete(w.cache, txid)
		evicted++
		newStart = i + 1
	}
	if newStart > 0 {
		w.evictOrder = w.evictOrder[newStart:]
	}
}

func combineTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata) []byte {
	mdBytes := metadata.Bytes()
	data := make([]byte, len(mdBytes)+len(txBytes))
	copy(data, mdBytes)
	copy(data[len(mdBytes):], txBytes)
	return data
}

// MustParseTxID extracts transaction ID from raw bytes.
func MustParseTxID(txBytes []byte) base.TransactionID {
	txid, err := transaction.IDFromParsedTransactionBytes(txBytes)
	if err != nil {
		panic(err)
	}
	return txid
}
