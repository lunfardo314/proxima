package tests

import (
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/stretchr/testify/require"
)

// TestReattachedVertexIsPrunedAgain: a reattached vertex must be tracked by the memDAG as live again and be
// detached again by the normal GC pass once its attacher has finished. Before, reattach turned the detached vertex
// back into a full vertex but its memDAG record stayed 'detached', which the normal pass skips: the vertex kept its
// past cone and dependencies until the size backstop, and through them everything older, and everything newer
// through their consumer edges. A reattach wave on the network therefore stalled pruning until the memDAG passed
// the backstop's cap.
//
// A sequencer runs a few slots under a ledger TTL of 2 slots, so its milestones are detached within seconds. A
// detached one is attached again, as a stale consumer makes a node do. The test also checks the active-attacher
// guard on a real milestone: a detach is declined while the attacher runs, and the forced detach bypasses it.
func TestReattachedVertexIsPrunedAgain(t *testing.T) {
	const (
		wallTTLSlots   = 30
		ledgerTTLSlots = 2
		nBranches      = 6
	)
	// deferred before the node's stop, so it is restored only after every attacher has ended
	defer memdag.SetGCTuningForTesting(50000, wallTTLSlots, ledgerTTLSlots)()
	testData := initWorkflowTest(t, 1, true) // memDAG GC on
	defer testData.stopAndWait()

	var (
		mu         sync.Mutex
		milestones []*vertex.WrappedTx // the references keep the detached vertices in the memDAG
	)
	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(nBranches))
	require.NoError(t, err)
	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		if ms.IsBranchTransaction() {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		milestones = append(milestones, ms)
	})
	exited := make(chan struct{})
	seq.OnExitOnce(func() { close(exited) })
	seq.Start()
	select {
	case <-exited:
	case <-time.After(60 * time.Second):
		t.Fatal("sequencer did not finish its branches")
	}
	mu.Lock()
	all := milestones
	mu.Unlock()
	require.NotEmpty(t, all, "no milestone captured")

	isDetached := func(vid *vertex.WrappedTx) (ret bool) {
		vid.RUnwrap(vertex.UnwrapOptions{DetachedVertex: func(_ *vertex.DetachedVertex) { ret = true }})
		return
	}
	// tracked as live: in the memDAG with its strong pointer, so the normal GC pass visits it
	trackedLive := func(vid *vertex.WrappedTx) bool {
		detachedInMap, found := testData.wrk.VerticesWithExpirationFlag()[vid]
		return found && !detachedInMap
	}

	var last *vertex.WrappedTx
	require.Eventually(t, func() bool {
		for i := len(all) - 1; i >= 0 && attacher.NumAttachers() == 0; i-- {
			if isDetached(all[i]) {
				last = all[i]
				return true
			}
		}
		return false
	}, 30*time.Second, 50*time.Millisecond, "memDAG GC did not detach a milestone")
	require.False(t, trackedLive(last), "a detached milestone must not be tracked as live")
	txBytes := last.GetTransaction().Bytes()

	// the guard on a real milestone: a separate vertex of the same transaction, 'started' and not finished
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	guarded := vertex.NewVertex(tx).Wrap()
	guarded.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted)
	require.False(t, guarded.ConvertToDetached(), "detach of a milestone with a running attacher must be declined")
	require.NotNil(t, guarded.GetVertex())
	guarded.ConvertToDetachedForced()
	require.Nil(t, guarded.GetVertex(), "the forced detach must bypass the guard")

	// reattach, as a stale consumer referencing it makes a node do
	t.Logf("%d milestones captured, reattaching %s", len(all), last.IDShortString())
	_, err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
	require.NoError(t, err)
	require.False(t, isDetached(last), "the milestone was not reattached")
	require.True(t, trackedLive(last), "a reattached milestone must be tracked as live again")

	require.Eventually(t, func() bool { return attacher.NumAttachers() == 0 },
		60*time.Second, 10*time.Millisecond, "the reattach did not finish")
	require.Equal(t, vertex.Good, last.GetTxStatus(), "the reattached milestone did not become Good")

	// the ledger TTL applies once the vertex has been in the memDAG a few slots after its reattach
	require.Eventually(t, func() bool { return isDetached(last) }, 120*time.Second, 100*time.Millisecond,
		"the reattached milestone was never detached again")
	require.False(t, trackedLive(last))
}
