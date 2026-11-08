package tests

import (
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/stretchr/testify/require"
)

// TestReattachIsCatchUpWork guards against the 2026-11-08 network halts. A sequencer resumed on
// its own chain output from 13-16 slots back, so every node reattached the milestones behind it,
// already detached by the memDAG GC (ledger TTL) but still inside the wall-clock TTL. One
// reattached milestone hit the reattach race in its consistency check and ended without a verdict,
// the hundreds of attachers built on it waited for it until the TTL, and because their milestones
// were inside the wall-clock TTL they counted toward the attacher cap: the nodes dropped every live
// milestone meanwhile.
//
// Here a sequencer runs a few slots under a ledger TTL of 2 slots and a wall-clock TTL of 30, so its
// milestones are detached within seconds and stay inside the wall-clock TTL for minutes. A detached
// one is attached again, as a stale consumer makes a node do. It must become Good long before the
// TTL, and its attacher must count as a past attacher, outside the cap. The race itself depends on
// timing between several sequencers' milestones and is not reproduced.
func TestReattachIsCatchUpWork(t *testing.T) {
	const (
		wallTTLSlots   = 30
		ledgerTTLSlots = 2
		nBranches      = 6
	)
	// deferred before the node's stop, so it is restored only after every attacher has ended
	defer memdag.SetGCTuningForTesting(50000, wallTTLSlots, ledgerTTLSlots)()
	testData := initWorkflowTest(t, 1, true) // memDAG GC on: the milestones must be detached
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
	// the chain tip stays in the memDAG; take the newest milestone the GC has detached
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
	require.Greater(t, last.Slot()+wallTTLSlots, ledger.TimeNow().Slot, "the milestone is already past the wall-clock TTL")
	t.Logf("%d milestones captured, reattaching %s", len(all), last.IDShortString())
	txBytes := last.GetTransaction().Bytes()

	// reattach it, as a stale consumer referencing it makes a node do
	_, err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
	require.NoError(t, err)

	// The attacher is counted synchronously, before its goroutine starts, so it is seen at once. It
	// must be a past attacher: only live attachers count toward the cap. It must finish well inside
	// the wall-clock TTL (30 slots, about 5 minutes).
	maxAtt, maxPast := attacher.NumAttachers(), attacher.NumPastAttachers()
	require.Eventually(t, func() bool {
		maxAtt, maxPast = max(maxAtt, attacher.NumAttachers()), max(maxPast, attacher.NumPastAttachers())
		return attacher.NumAttachers() == 0
	}, 60*time.Second, 5*time.Millisecond, "the reattach did not finish")
	t.Logf("reattach: up to %d attachers, %d of them past attachers", maxAtt, maxPast)
	require.Positive(t, maxAtt)
	require.Equal(t, maxAtt, maxPast, "a reattach attacher counted toward the attacher cap")

	require.Equal(t, vertex.Good, last.GetTxStatus(), "the reattached milestone did not become Good")
}
