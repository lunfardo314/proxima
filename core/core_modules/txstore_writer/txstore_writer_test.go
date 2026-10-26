package txstore_writer

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// closableStore stands in for the transaction store: once closed, any use panics, as the
// database-backed store does after the node has closed its database
type closableStore struct {
	closed atomic.Bool
	used   atomic.Int32
}

func (s *closableStore) check() {
	if s.closed.Load() {
		s.used.Add(1)
		panic("store used after it was closed")
	}
}

func (s *closableStore) GetTxBytes(_ *base.TransactionID) []byte { s.check(); return nil }
func (s *closableStore) HasTxBytes(_ *base.TransactionID) bool   { s.check(); return false }
func (s *closableStore) PersistTxBytes(_ []byte, txid ...base.TransactionID) (base.TransactionID, error) {
	s.check()
	return txid[0], nil
}
func (s *closableStore) PersistTxBytesBatch(_ map[base.TransactionID][]byte) error {
	s.check()
	return nil
}

// TestNoWriteAfterStop covers the shutdown order: the writer flushes once when the node stops,
// then the store is closed. A transaction queued after that must neither be written nor arm the
// flush timer, which would fire against the closed store and panic.
func TestNoWriteAfterStop(t *testing.T) {
	privKey := ledger.InitWithTestingLedgerData()
	t.Cleanup(ledger.ResetForTesting)
	u := utxodb.NewUTXODB(privKey, true)
	_, _, addr := u.GenerateAddress(1)
	txBytes, err := u.MakeTransactionFromFaucet(addr)
	require.NoError(t, err)
	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	glb := global.NewDefault()
	store := &closableStore{}
	w := New(glb, store)

	glb.Stop()
	require.True(t, glb.WaitAllWorkProcessesStop(5*time.Second))
	store.closed.Store(true)

	// a straggler arriving after the final flush, as the input queue can still deliver one
	w.PersistTxBytesQueued(tx)
	time.Sleep(maxFlushDelay + 300*time.Millisecond)

	require.Zero(t, store.used.Load(), "the closed store was used")
	require.Zero(t, w.PendingCount())
}
