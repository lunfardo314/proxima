package attacher

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// solicitCountingEnv finds every dependency in the txstore and counts what reaches the solicit queue.
// Only the methods on the local-solicit path are implemented; anything else panics on the nil interface.
type solicitCountingEnv struct {
	Environment
	pushed int
}

func (e *solicitCountingEnv) TxPullParameters() (time.Duration, int) { return time.Minute, 30 }
func (e *solicitCountingEnv) TakeCachedTx(*base.TransactionID) *transaction.Transaction {
	return nil
}
func (e *solicitCountingEnv) GetTxBytes(*base.TransactionID) []byte { return []byte{1} }
func (e *solicitCountingEnv) TxBytesFromStoreInSolicited([]byte)    { e.pushed++ }
func (e *solicitCountingEnv) CachedTxInSolicited(*transaction.Transaction) {
	e.pushed++
}

// A dependency found in the txstore is queued once per pull period, however many passes the
// attachers waiting on it make. Before, each pass of each attacher queued another copy, and a
// reattach burst of thousands of attachers filled the solicit queue with millions of copies.
func TestLocalSolicitQueuedOncePerPullPeriod(t *testing.T) {
	env := &solicitCountingEnv{}
	tip := vertex.WrapTxID(base.RandomTransactionID(true, 1, base.LedgerTime{Slot: 10, Tick: 50}))
	dep := vertex.WrapTxID(base.RandomTransactionID(false, 0, base.LedgerTime{Slot: 10, Tick: 20}))

	newAttacher := func() *attacher {
		return &attacher{
			Environment: env,
			pastCone:    vertex.NewPastCone(nil, tip, tip.Timestamp(), "test"),
		}
	}
	// several attachers wait on the same dependency, each polling it many times
	for i := 0; i < 5; i++ {
		a := newAttacher()
		for j := 0; j < 100; j++ {
			require.True(t, a.pullIfNeeded(dep))
		}
	}
	require.Equal(t, 1, env.pushed)

	// the proposer's non-blocking solicit respects the same pull period
	a := newAttacher()
	a.noPull = true
	for j := 0; j < 100; j++ {
		a.pullIfNeeded(dep)
	}
	require.Equal(t, 1, env.pushed)
}
