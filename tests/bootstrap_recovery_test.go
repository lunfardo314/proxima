package tests

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// TestRestartedSequencerRejoinsThroughBootstrap is honest recovery across nodes, the case v1.7.2 broke on
// 2026-11-20: a node whose sequencer restarts while the rest of the network keeps branching recovers through
// bootstrap transactions, which the other nodes must take, and rejoins.
//
// Node A is the network: the bootstrap sequencer, branching. Node B runs sequencer S, also branching, and the
// two gossip to each other over one shared transaction store, pulls answered from it as a peer would. B goes
// down for offlineSlots and restarts on its own committed state, by then that many slots old. It gossips to A
// from the start, but hears A only after S has issued its first bootstrap transaction on its stale latest
// reliable branch, as a restarted node does before its peers' branches reach it. A, branching, drops the
// bootstrap transaction at reception; S's next milestones pull it in. S must be included by A again and win
// one of A's branches within rejoinSlots. Both nodes run memDAG GC.
func TestRestartedSequencerRejoinsThroughBootstrap(t *testing.T) {
	const (
		seqPace      = 5
		offlineSlots = 6
		rejoinSlots  = 30
	)
	reinitTestLedger()
	require.True(t, ledger.L(base.MaxSlot).EnforceCoverageDeltaMonotonicity,
		"coverageDelta monotonicity must be enabled for sequencer tests")

	sharedTxStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
	linkA := &gossipLink{from: "nodeA"}

	// ---------- node A: the network ----------
	nodeA := initWorkflowTestOnNetwork(t, 1, sharedTxStore, linkA.peers(), true)
	nodeA.env.answerPull = answerPullFrom(sharedTxStore, nodeA.wrk)
	require.NoError(t, nodeA.wrk.EnsureLatestBranches())
	nodeA.makeChainOrigins(1)
	chainOriginsTxID, err := nodeA.txBytesInForTestsChainOrigins()
	require.NoError(t, err)
	sChainID := nodeA.chainOrigins[0].ChainID

	nodeA.bootstrapSeq, err = newTestSequencer(nodeA.wrk, nodeA.bootstrapChainID, genesisPrivateKey,
		sequencer.WithName("boot"),
		sequencer.WithPace(seqPace),
	)
	require.NoError(t, err)
	nodeA.bootstrapSeq.Start()
	defer nodeA.bootstrapSeq.Stop()
	_, err = nodeA.wrk.WaitUntilTransactionInHeaviestState(chainOriginsTxID, 30*time.Second)
	require.NoError(t, err)
	stateStoreA := nodeA.env.stateStore.(*common.InMemoryKVStore)

	// startB starts node B on the given committed state, with S on it. B gossips to A from the start; whether it
	// hears A is up to the caller (linkA.add).
	startB := func(stateStore *common.InMemoryKVStore, opts ...sequencer.ConfigOption) (*workflow.Workflow, *workflowDummyEnvironment, testSequencer) {
		linkB := &gossipLink{from: "nodeB"}
		linkB.add(nodeA.wrk)
		envB := newWorkflowDummyEnvironment(stateStore, sharedTxStore)
		envB.lrbFromStore.Store(true)
		wrkB := workflow.Start(envB, linkB.peers(), workflow.OptionMaxConcurrentAttachers(200))
		envB.answerPull = answerPullFrom(sharedTxStore, wrkB)
		require.NoError(t, wrkB.EnsureLatestBranches())
		seqS, err := newTestSequencer(wrkB, sChainID, nodeA.privKeyAux, append([]sequencer.ConfigOption{
			sequencer.WithName("S"),
			sequencer.WithPace(seqPace),
			sequencer.WithProduceBranches,
		}, opts...)...)
		require.NoError(t, err)
		return wrkB, envB, seqS
	}
	lrbA := func() *multistate.BranchData {
		lrb := nodeA.wrk.Branches().FindLatestReliableBranch()
		require.NotNil(t, lrb)
		return lrb
	}
	// waitSBranchOnA waits until A's latest reliable branch is a branch of S after the given slot
	waitSBranchOnA := func(afterSlot uint32, within int, what string) uint32 {
		deadline := ledger.TimeNow().Slot + uint32(within)
		for {
			if lrb := lrbA(); lrb.SequencerID == sChainID && lrb.Stem.ID.Slot() > afterSlot {
				return lrb.Stem.ID.Slot()
			}
			require.True(t, ledger.TimeNow().Slot < deadline, "%s: no branch of S became A's latest reliable branch within %d slots", what, within)
			time.Sleep(ledger.TickDuration())
		}
	}

	// ---------- B with S joins the running network ----------
	wrkB, envB, seqS := startB(copyStateStore(stateStoreA))
	linkA.add(wrkB)
	seqS.Start()
	firstSlot := waitSBranchOnA(0, rejoinSlots, "before the restart")
	t.Logf("S won A's branch at slot %d", firstSlot)

	// ---------- B goes down ----------
	seqS.Stop()
	envB.Stop()
	linkA.mutex.Lock()
	linkA.to = nil
	linkA.mutex.Unlock()
	stateStoreB := copyStateStore(envB.stateStore.(*common.InMemoryKVStore))
	downSlot := ledger.TimeNow().Slot
	for ledger.TimeNow().Slot < downSlot+offlineSlots {
		time.Sleep(ledger.TickDuration())
	}

	// ---------- B restarts on its own state, now stale ----------
	// The sequencer's loop must begin in the early ticks of a slot, where bootstrap transactions are issued:
	// begun at the slot edge, its branch proposer extends its stale branch instead (a valid path out of the gap,
	// but not this one). Before the loop it listens for the peers' branches, polling every 2 s, then waits
	// DelayStart: started at a slot boundary, the delay below puts the loop's start at tick 14.
	slot, tick := ledger.SlotDuration(), ledger.TickDuration()
	delayStart := 2*slot - (2*time.Second)%slot + 14*tick
	for ledger.TimeNow().Slot < downSlot+offlineSlots+1 {
		time.Sleep(ledger.TickDuration())
	}
	wrkB2, envB2, seqS2 := startB(stateStoreB, sequencer.WithDelayStart(delayStart))
	defer envB2.Stop()
	defer seqS2.Stop()
	restartSlot := ledger.TimeNow().Slot
	var firstBootstrap atomic.Pointer[base.TransactionID]
	var nBootstrap atomic.Int32
	seqS2.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		if ms.IsBootstrapMode() {
			nBootstrap.Add(1)
			txid := ms.ID()
			firstBootstrap.CompareAndSwap(nil, &txid)
		}
	})
	seqS2.Start()
	deadline := time.Now().Add(5 * ledger.SlotDuration())
	for firstBootstrap.Load() == nil {
		require.True(t, time.Now().Before(deadline), "the restarted sequencer did not recover through a bootstrap transaction")
		time.Sleep(ledger.TickDuration())
	}
	bootstrapID := *firstBootstrap.Load()
	require.True(t, bootstrapID.Slot() >= restartSlot)
	// the restarted node's peers' branches reach it now
	linkA.add(wrkB2)
	t.Logf("S restarted at slot %d, first bootstrap transaction %s, network LRB slot %d", restartSlot, bootstrapID.StringShort(), lrbA().Stem.ID.Slot())

	// ---------- S must rejoin: included by A again and winning one of A's branches ----------
	rejoinSlot := waitSBranchOnA(restartSlot, rejoinSlots, "after the restart")
	rdrA := multistate.MakeSugared(nodeA.wrk.Branches().GetStateReaderForTheBranch(lrbA().Stem.ID.TransactionID()))
	committed := rdrA.KnowsCommittedTransaction(bootstrapID)
	t.Logf("S rejoined: its branch is A's latest reliable branch at slot %d, %d slots after the restart; bootstrap transactions issued %d, the first committed on A: %v; A dropped at reception: %d; B LRB slot %d",
		rejoinSlot, rejoinSlot-restartSlot, nBootstrap.Load(), committed, nodeA.wrk.Counter("bootstrap_drop"), wrkB2.Branches().FindLatestReliableBranch().Stem.ID.Slot())

}
