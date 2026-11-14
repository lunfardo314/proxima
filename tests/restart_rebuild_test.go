package tests

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// gossipLink carries a node's gossip to the nodes added to it, into their input queues as gossip
// from a peer. It is one-way: a node hears another only through that node's link.
type gossipLink struct {
	from  peer.ID
	mutex sync.RWMutex
	to    []*workflow.Workflow
}

func (l *gossipLink) add(w *workflow.Workflow) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.to = append(l.to, w)
}

func (l *gossipLink) peers() *peering.Peers {
	return peering.NewPeersDummyGossiping(func(txBytes []byte, txid base.TransactionID) {
		l.mutex.RLock()
		defer l.mutex.RUnlock()
		for _, w := range l.to {
			w.TxBytesInFromPeerQueued(txBytes, nil, l.from, txid)
		}
	})
}

// answerPullFrom answers a node's pulls from the shared store, as a peer holding every transaction
// would: a transaction still in its writer's buffer is waited for, the reply arrives as a pulled one.
func answerPullFrom(store global.TxBytesStore, w *workflow.Workflow) func(txid base.TransactionID) {
	return func(txid base.TransactionID) {
		go func() {
			for i := 0; i < 20; i++ {
				if txBytes := store.GetTxBytes(&txid); len(txBytes) > 0 {
					w.TxBytesInFromPeerQueued(txBytes, nil, "peer", txid)
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
	}
}

// copyStateStore is the committed state of a node at the moment of the copy, as a restarted
// node finds it in its database. A commit and the iteration both hold the store's lock, so the
// copy is consistent.
func copyStateStore(src *common.InMemoryKVStore) *common.InMemoryKVStore {
	ret := common.NewInMemoryKVStore()
	src.Iterate(func(k, v []byte) bool {
		ret.Set(k, v)
		return true
	})
	return ret
}

// TestRestartRebuildsIgnoredChain reproduces the restart stall of 2026-11-12 (hboot): a node
// restarted while a sequencer nobody includes keeps issuing milestones falls behind the network
// for minutes.
//
// The nodes run in one process, linked by gossip, and share one transaction store. Node A is the
// network: the bootstrap sequencer, the only branch producer, and nOtherSequencers sequencers
// adding milestone traffic. Each of nIgnored nodes C runs one sequencer producing no branches;
// the first is X, the one measured. The links are one-way, A to each C, so an ignored sequencer
// endorses A's branches while nobody consumes its chain output, and every new milestone of it
// carries its whole unincluded chain in its past-cone delta. A running node takes that chain one
// milestone at a time. With a single branch producer an ignored sequencer always endorses the
// branch that wins; with competing branches it would restart from its committed output whenever
// it backed a loser, and its unincluded chain would stay a few slots deep.
//
// After the chain has grown for ignoredSlots, node B starts as a restart of A: a copy of A's
// committed state, an empty memDAG, the shared transaction store and memDAG GC on. From then on
// A and C gossip to B, and B's pulls are answered from the shared store as a peer would. The
// test samples B every slot: its live and past attachers, the sequencer transactions its input
// gate refused, the reattaches, its memDAG size, the oldest X milestone it holds (how deep the
// rebuild went) and its latest reliable branch against A's.
//
// The attacher cap is scaled to the traffic: hboot's 200 is about one slot of its gossip, and
// attacherCap is about one slot of this test's. NumAttachers and NumPastAttachers, which the gate
// reads, are process-wide, so A's and C's attachers count against B's cap too; their number is
// logged before B starts. The other counters are B's own.
//
// At this size the rebuild does not stall B: the chain is rebuilt within a slot, the burst of
// attachers passes the cap for a moment and B is back in sync in about two slots. The test
// guards that, and is the bench on which to grow the scenario toward the stall.
func TestRestartRebuildsIgnoredChain(t *testing.T) {
	const (
		ignoredSlots     = 120 // slots X's chain grows unincluded before the restart
		nOtherSequencers = 3   // sequencers on A besides the bootstrap one, producing no branches
		nIgnored         = 6   // sequencers nobody includes, each on its own node
		observeSlots     = 30  // slots B is watched after the restart
		inSyncSlots      = 1   // B is back in sync when its LRB is at most this many slots behind A's
		maxResyncSlots   = 10  // B must be back in sync within this many slots of the restart
		attacherCap      = 20
		seqPace          = 5
	)
	reinitTestLedger()
	require.True(t, ledger.L(base.MaxSlot).EnforceCoverageDeltaMonotonicity,
		"coverageDelta monotonicity must be enabled for sequencer tests")

	sharedTxStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
	linkA := &gossipLink{from: "nodeA"}

	// ---------- node A: the network, with X's chain origin committed ----------
	nodeA := initWorkflowTestOnNetwork(t, nOtherSequencers+nIgnored, sharedTxStore, linkA.peers(), true)
	require.NoError(t, nodeA.wrk.EnsureLatestBranches())
	nodeA.makeChainOrigins(nOtherSequencers + nIgnored)
	chainOriginsTxID, err := nodeA.txBytesInForTestsChainOrigins()
	require.NoError(t, err)
	xChainID := nodeA.chainOrigins[nOtherSequencers].ChainID

	nodeA.bootstrapSeq, err = newTestSequencer(nodeA.wrk, nodeA.bootstrapChainID, genesisPrivateKey,
		sequencer.WithName("boot"),
		sequencer.WithPace(seqPace),
	)
	require.NoError(t, err)
	nodeA.bootstrapSeq.Start()
	defer nodeA.bootstrapSeq.Stop()

	_, err = nodeA.wrk.WaitUntilTransactionInHeaviestState(chainOriginsTxID, 30*time.Second)
	require.NoError(t, err)
	for i := 0; i < nOtherSequencers; i++ {
		seq, err := newTestSequencer(nodeA.wrk, nodeA.chainOrigins[i].ChainID, nodeA.privKeyAux,
			sequencer.WithName(fmt.Sprintf("seq%d", i)),
			sequencer.WithPace(seqPace),
		)
		require.NoError(t, err)
		seq.Start()
		defer seq.Stop()
	}
	stateStoreA := nodeA.env.stateStore.(*common.InMemoryKVStore)

	// ---------- nodes C: one ignored sequencer each, hearing A, never heard by A ----------
	// Each ignored sequencer runs on a node of its own, as on the network: on a shared node they
	// would endorse each other and fork each other's chains.
	ignoredLinks := make([]*gossipLink, nIgnored)
	ignoredNodes := make([]*workflow.Workflow, nIgnored)
	ignoredSeqs := make([]testSequencer, nIgnored)
	for i := range ignoredNodes {
		ignoredLinks[i] = &gossipLink{from: peer.ID(fmt.Sprintf("nodeC%d", i))}
		envC := newWorkflowDummyEnvironment(copyStateStore(stateStoreA), sharedTxStore)
		ignoredNodes[i] = workflow.Start(envC, ignoredLinks[i].peers(), workflow.OptionDisableMemDAGGC, workflow.OptionMaxConcurrentAttachers(200))
		envC.answerPull = answerPullFrom(sharedTxStore, ignoredNodes[i])
		defer envC.Stop()
		require.NoError(t, ignoredNodes[i].EnsureLatestBranches())
		linkA.add(ignoredNodes[i])

		ignoredSeqs[i], err = newTestSequencer(ignoredNodes[i], nodeA.chainOrigins[nOtherSequencers+i].ChainID, nodeA.privKeyAux,
			sequencer.WithName(fmt.Sprintf("ign%d", i)),
			sequencer.WithPace(seqPace),
		)
		require.NoError(t, err)
		defer ignoredSeqs[i].Stop()
	}
	// X is the first of them, the one measured
	wrkC, seqX := ignoredNodes[0], ignoredSeqs[0]

	// every milestone of X with its chain predecessor, to measure the unincluded chain
	var xMutex sync.Mutex
	xPred := make(map[base.TransactionID]base.TransactionID)
	var xTip base.TransactionID
	seqX.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		tx := ms.GetTransaction()
		if tx == nil {
			return
		}
		pred, _, err := tx.SequencerChainPredecessor()
		require.NoError(t, err)
		xMutex.Lock()
		defer xMutex.Unlock()
		xPred[ms.ID()] = pred.TransactionID()
		xTip = ms.ID()
	})
	for _, seq := range ignoredSeqs {
		seq.Start()
	}

	// the unincluded chain behind X's latest milestone: milestones and the slot it starts at
	xChain := func() (tip base.TransactionID, length int, fromSlot uint32) {
		xMutex.Lock()
		defer xMutex.Unlock()
		tip = xTip
		for id, ok := tip, true; ok; id, ok = xPred[id] {
			length++
			fromSlot = id.Slot()
		}
		return
	}
	lrbSlot := func(w *workflow.Workflow) uint32 {
		if lrb := w.Branches().FindLatestReliableBranch(); lrb != nil {
			return lrb.Stem.ID.Slot()
		}
		return 0
	}
	// slot of X's chain output in A's latest reliable state: the origin's while nobody includes X
	xOutputSlotOnA := func() uint32 {
		lrb := nodeA.wrk.Branches().FindLatestReliableBranch()
		require.NotNil(t, lrb)
		rdr := multistate.MustNewSugaredReadableState(stateStoreA, lrb.Root, 0)
		o, err := rdr.GetChainOutputWithID(xChainID)
		require.NoError(t, err)
		return o.ID.Slot()
	}

	// ---------- grow X's unincluded chain ----------
	startSlot := ledger.TimeNow().Slot
	for ledger.TimeNow().Slot < startSlot+ignoredSlots {
		time.Sleep(10 * ledger.SlotDuration())
		tip, length, fromSlot := xChain()
		t.Logf("slot %d: A LRB slot %d, C LRB slot %d, X tip %s, unincluded chain %d milestones from slot %d, attachers %d (past %d)",
			ledger.TimeNow().Slot, lrbSlot(nodeA.wrk), lrbSlot(wrkC), tip.StringShort(), length, fromSlot,
			attacher.NumAttachers(), attacher.NumPastAttachers())
	}
	_, chainLength, chainFromSlot := xChain()
	require.Less(t, xOutputSlotOnA(), startSlot, "X was included by A: the one-way link leaked")

	// ---------- node B: restart of A ----------
	envB := newWorkflowDummyEnvironment(copyStateStore(stateStoreA), sharedTxStore)
	envB.lrbFromStore.Store(true)
	restartedAt := time.Now()
	restartSlot := ledger.TimeNow().Slot
	restartLRBSlot := multistate.FindLatestReliableBranch(envB.stateStore).Stem.ID.Slot()
	t.Logf("RESTART at slot %d: X chain %d milestones from slot %d; process-wide attachers before B: %d (past %d), A LRB slot %d, B committed LRB slot %d",
		restartSlot, chainLength, chainFromSlot, attacher.NumAttachers(), attacher.NumPastAttachers(), lrbSlot(nodeA.wrk), restartLRBSlot)
	wrkB := workflow.Start(envB, peering.NewPeersDummy(), workflow.OptionMaxConcurrentAttachers(attacherCap))
	envB.answerPull = answerPullFrom(sharedTxStore, wrkB)
	defer envB.Stop()
	linkA.add(wrkB)
	for _, l := range ignoredLinks {
		l.add(wrkB)
	}

	// the X milestones in B's memDAG and the oldest slot among them
	xInB := func() (n int, oldest uint32) {
		xMutex.Lock()
		defer xMutex.Unlock()
		for id := range xPred {
			if wrkB.GetVertex(id) != nil {
				if n == 0 || id.Slot() < oldest {
					oldest = id.Slot()
				}
				n++
			}
		}
		return
	}

	var (
		inSyncAfter        time.Duration
		maxAtt, maxAttPast int
		maxVertices        int
		oldestX            = restartSlot
	)
	for ledger.TimeNow().Slot < restartSlot+observeSlots {
		time.Sleep(ledger.SlotDuration())
		att, attPast := envB.Counter("att"), envB.Counter("att_past")
		maxAtt, maxAttPast = max(maxAtt, att), max(maxAttPast, attPast)
		maxVertices = max(maxVertices, wrkB.NumVertices())
		slotA, slotB := lrbSlot(nodeA.wrk), lrbSlot(wrkB)
		if inSyncAfter == 0 && slotB > restartLRBSlot && slotB+inSyncSlots >= slotA {
			inSyncAfter = time.Since(restartedAt)
		}
		nX, oldest := xInB()
		if nX > 0 {
			oldestX = min(oldestX, oldest)
		}
		t.Logf("B +%d slots: att %d (past %d), gate total %d (past %d), seq_drop %d, old_seq_drop %d, old_solicit %d, reattach %d, vertices %d, X milestones %d from slot %d, LRB A %d B %d",
			ledger.TimeNow().Slot-restartSlot, att, attPast, attacher.NumAttachers(), attacher.NumPastAttachers(),
			envB.Counter("seq_drop"), envB.Counter("old_seq_drop"), envB.Counter("old_solicit"), envB.Counter("reattach"), wrkB.NumVertices(),
			nX, oldest, slotA, slotB)
	}
	t.Logf("SUMMARY: X chain %d milestones from slot %d at the restart; B rebuilt X back to slot %d; back in sync after %v (0 = never); peak att %d (past %d), seq_drop %d, reattach %d, peak vertices %d, B behind A by %d slots at the end",
		chainLength, chainFromSlot, oldestX, inSyncAfter, maxAtt, maxAttPast,
		envB.Counter("seq_drop"), envB.Counter("reattach"), maxVertices, int(lrbSlot(nodeA.wrk))-int(lrbSlot(wrkB)))
	require.True(t, inSyncAfter > 0 && inSyncAfter <= maxResyncSlots*ledger.SlotDuration(),
		"the restarted node was not back in sync within %d slots (after %v)", maxResyncSlots, inSyncAfter)
}
