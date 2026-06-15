package tests

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// TestMemDAGLaggingNodeRecursion reproduces the unbounded recursive attachment /
// past-cone growth that wedged loc0/seq1/loc1 on 2026-06-14 (depth 900+, walking
// backward through slots, giant past cone, memDAG that never prunes / heals).
//
// Root mechanism under test: the attacher depth cap (vertex.MaxAttachmentDepthForPull)
// only gates network PULLS (core/attacher/pull.go). When a far-behind ("lagging")
// node already has all the transactions locally in its txstore (gossiped during a
// high-load burst but not yet committed), a single far-ahead sequencer milestone
// makes the attacher walk the WHOLE branch chain back to genesis via txstore
// look-ups — bypassing the depth cap, which is supposed to defer deep catch-up to
// forward-sync. The result is a past cone / memDAG proportional to the entire gap.
//
// Setup:
//
//	node A: bootstrap sequencer produces N (>> depth cap) branches. Every tx lands
//	        in A's txstore; A commits them all to A's state.
//	node B: fresh genesis state (same globals → same genesis), SHARING A's txstore,
//	        forward-sync idle (no sources). This is the lagging node: committed state
//	        at genesis, but every tx available locally.
//
// Action: attach A's far-ahead tip milestone to B (simulating gossip).
//
// Expectation (the FIX target): a lagging node must NOT recursively materialize the
// whole chain from one far-ahead tx — recursion is bounded by the depth cap and the
// deep tail is left to forward-sync. So B's memDAG should stay bounded (a few slots'
// worth), NOT grow to ~the entire N-slot chain.
//
// On CURRENT code this assertion FAILS: B re-walks the entire chain. The test
// documents the bug and will guard the fix.
func TestMemDAGLaggingNodeRecursion(t *testing.T) {
	const nBranches = 60 // >> vertex.MaxAttachmentDepthForPull (50)

	t.Logf("depth cap (MaxAttachmentDepthForPull) = %d; target branches = %d", vertex.MaxAttachmentDepthForPull, nBranches)

	// ---------- node A: generate a valid N-slot sequencer+branch chain ----------
	nodeA := initWorkflowTest(t, 1, true)

	// capture the latest NON-branch milestone (a realistic "far-ahead gossip tx")
	var tipMu sync.Mutex
	var tipBytes []byte
	var tipID base.TransactionID
	var tipSlot uint32
	var brCount atomic.Int32

	seq, err := newTestSequencer(nodeA.wrk, nodeA.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(nBranches))
	require.NoError(t, err)
	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		if ms.IsBranchTransaction() {
			brCount.Add(1)
			return
		}
		if tx := ms.GetTransaction(); tx != nil {
			tipMu.Lock()
			tipBytes = tx.Bytes()
			tipID = ms.ID()
			tipSlot = ms.Slot()
			tipMu.Unlock()
		}
	})
	seq.OnExitOnce(func() { nodeA.stop() })
	seq.Start()
	nodeA.waitStop()

	require.GreaterOrEqual(t, int(brCount.Load()), nBranches/2,
		"node A did not produce enough branches to exceed the depth cap")

	tipMu.Lock()
	tb := tipBytes
	tid := tipID
	tslot := tipSlot
	tipMu.Unlock()
	require.NotNil(t, tb, "no non-branch milestone captured from node A")

	t.Logf("node A: produced %d branches; tip milestone %s at slot %d; A memDAG vertices=%d",
		brCount.Load(), tid.StringShort(), tslot, nodeA.wrk.NumVertices())

	// ---------- node B: the lagging node — fresh genesis state, SHARED txstore ----------
	stateStoreB := common.NewInMemoryKVStore()
	multistate.InitStateStoreFromGlobals(stateStoreB) // same genesis as A (deterministic)
	envB := newWorkflowDummyEnvironment(stateStoreB, nodeA.txStore)
	// GC disabled on B so NumVertices deterministically reflects everything the
	// recursive walk materialized (GC churn is a separate concern from over-attachment).
	wrkB := workflow.Start(envB, peering.NewPeersDummy(),
		workflow.OptionDisableMemDAGGC, workflow.OptionMaxConcurrentAttachers(200))
	defer envB.Stop()

	t.Logf("node B: memDAG vertices before attach=%d", wrkB.NumVertices())

	// ---------- action: attach the far-ahead tip to the lagging node ----------
	// settleWait must outlast the BUG's full recursive build (which completes in a
	// few seconds) so the guard still trips if the bound regresses, while the FIX
	// case settles at its bounded size near-instantly and simply stalls here.
	const settleWait = 25 * time.Second
	done := make(chan error, 1)
	_, err = attacher.AttachTransactionFromBytes(tb, wrkB, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, e error) {
		done <- e
	}))
	require.NoError(t, err)

	select {
	case e := <-done:
		t.Logf("node B: tip attachment finished, err=%v", e)
	case <-time.After(settleWait):
		t.Logf("node B: tip attachment still stalled after %v (expected with fix: deep tail deferred to forward-sync)", settleWait)
	}

	nB := wrkB.NumVertices()
	t.Logf("node B: memDAG vertices AFTER attaching far-ahead tip = %d", nB)

	// The depth cap should bound recursive catch-up; deep gaps are forward-sync's job.
	// With the fix, B materializes ≈ depth-cap worth (~52 vertices, independent of
	// chain length); without it, B pulls the whole chain (≈ 2*nBranches). The bound
	// sits between the two.
	const bound = 90
	require.Lessf(t, nB, bound,
		"lagging node recursively materialized %d vertices from a single far-ahead milestone "+
			"(chain was %d branches); the depth cap did not bound the txstore-backed walk", nB, nBranches)
}
