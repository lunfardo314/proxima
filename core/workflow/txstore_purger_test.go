package workflow

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The purge floor follows only sealed branches the node has committed, never goes down, and the
// purger deletes whole slots strictly below it, oldest first, leaving everything from the floor on.
func TestTxStorePurger(t *testing.T) {
	env := newWorkflowDummyEnvironment()
	store := env.txBytesStore.(*txstore.SimpleTxBytesStore)
	const perSlot = 3
	for slot := uint32(100); slot <= 110; slot++ {
		for i := 0; i < perSlot; i++ {
			id := base.RandomTransactionID(false, byte(i), base.T(slot, 5))
			_, err := store.PersistTxBytes([]byte{byte(i)}, id)
			require.NoError(t, err)
		}
	}
	w := &Workflow{environment: env, purger: &txStorePurger{
		keepSlots:   5,
		floorGauge:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_floor"}),
		slotsPurged: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_purged"}),
	}}

	committed := base.RandomTransactionID(true, 0, base.T(108, 0))
	uncommitted := base.RandomTransactionID(true, 1, base.T(110, 0))
	olderCommitted := base.RandomTransactionID(true, 2, base.T(106, 0))
	// any valid root record makes a branch committed; the genesis one serves
	rr := multistate.FetchAnyLatestRootRecord(env.stateStore)
	batch := env.stateStore.BatchedWriter()
	multistate.WriteRootRecord(batch, committed, rr)
	multistate.WriteRootRecord(batch, olderCommitted, rr)
	require.NoError(t, batch.Commit())

	w.raisePurgeFloor(uncommitted) // not on this node's lineage: ignored
	require.Zero(t, w.purger.floor.Load())
	w.raisePurgeFloor(committed)
	require.EqualValues(t, 103, w.purger.floor.Load())
	w.raisePurgeFloor(olderCommitted) // the floor only rises
	require.EqualValues(t, 103, w.purger.floor.Load())

	w.purgeRound(store)
	oldest, found := txstore.OldestSlot(store)
	require.True(t, found)
	require.EqualValues(t, 103, oldest)
	latest, _ := txstore.LatestSlot(store)
	require.EqualValues(t, 110, latest)
	for slot := uint32(103); slot <= 110; slot++ {
		n := 0
		store.Iterator(base.Slot2Bytes(slot)).IterateKeys(func([]byte) bool { n++; return true })
		require.Equal(t, perSlot, n, "slot %d lost transactions", slot)
	}

	// a further round with nothing below the floor deletes nothing
	w.purgeRound(store)
	oldest, _ = txstore.OldestSlot(store)
	require.EqualValues(t, 103, oldest)
}
