// Package txstore_writer implements batched asynchronous writes to the transaction store.
//
// Instead of writing each transaction individually (one BadgerDB transaction per write),
// this module collects writes in a buffer and flushes them as a single batch.
// This reduces BadgerDB write amplification and compaction pressure under high TPS.
//
// Reads are not affected — they go directly to the underlying store.
// The buffer is flushed when it reaches maxBatchSize items or maxFlushDelay elapses.
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
)

type (
	environment interface {
		global.NodeGlobal
	}

	TxStoreWriter struct {
		environment
		store global.TxBytesStore

		mu      sync.Mutex
		pending map[base.TransactionID][]byte
		timer   *time.Timer
	}
)

func New(env environment, store global.TxBytesStore) *TxStoreWriter {
	ret := &TxStoreWriter{
		environment: env,
		store:       store,
		pending:     make(map[base.TransactionID][]byte, maxBatchSize),
	}

	env.MarkWorkProcessStarted(Name)
	go func() {
		<-env.Ctx().Done()
		// flush remaining on shutdown
		ret.flush()
		env.MarkWorkProcessStopped(Name)
		env.Log().Infof("[%s] STOPPED", Name)
	}()

	env.Log().Infof("[%s] STARTED (batch size: %d, flush delay: %v)", Name, maxBatchSize, maxFlushDelay)
	return ret
}

// PersistTxBytesQueued adds a transaction to the write buffer.
// The actual DB write happens when the buffer is full or the flush timer fires.
func (w *TxStoreWriter) PersistTxBytesQueued(txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID) {
	mdBytes := metadata.Bytes()
	data := make([]byte, len(mdBytes)+len(txBytes))
	copy(data, mdBytes)
	copy(data[len(mdBytes):], txBytes)

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.pending[txid]; exists {
		return // already buffered
	}
	// also skip if already in the store
	if w.store.HasTxBytes(&txid) {
		return
	}

	w.pending[txid] = data

	if len(w.pending) >= maxBatchSize {
		w.flushLocked()
		return
	}
	// start flush timer on first item
	if w.timer == nil {
		w.timer = time.AfterFunc(maxFlushDelay, func() {
			w.flush()
		})
	}
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
	if len(w.pending) == 0 {
		return
	}

	batch := w.pending
	w.pending = make(map[base.TransactionID][]byte, maxBatchSize)

	if err := w.store.PersistTxBytesBatch(batch); err != nil {
		w.Log().Errorf("[%s] batch write failed (%d items): %v", Name, len(batch), err)
		// fallback: try individual writes
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

// HasPending checks if a transaction is in the write buffer (not yet flushed).
func (w *TxStoreWriter) HasPending(txid *base.TransactionID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, exists := w.pending[*txid]
	return exists
}

// GetPending returns buffered data for a transaction, or nil if not buffered.
func (w *TxStoreWriter) GetPending(txid *base.TransactionID) []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pending[*txid]
}

// PendingCount returns the number of items in the write buffer.
func (w *TxStoreWriter) PendingCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

// MustPersistTxBytesQueued is a convenience wrapper that parses the txid if not provided.
func MustPersistTxBytesQueued(w *TxStoreWriter, txBytes []byte, metadata *txmetadata.TransactionMetadata, txid base.TransactionID) {
	if metadata == nil {
		metadata = &txmetadata.TransactionMetadata{}
	}
	w.PersistTxBytesQueued(txBytes, metadata, txid)
}

// MustParseTxID extracts transaction ID from raw bytes.
func MustParseTxID(txBytes []byte) base.TransactionID {
	txid, err := transaction.IDFromParsedTransactionBytes(txBytes)
	if err != nil {
		panic(err)
	}
	return txid
}
