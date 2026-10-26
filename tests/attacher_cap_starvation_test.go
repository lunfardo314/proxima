package tests

import (
	"crypto/ed25519"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// TestPastAttachersDoNotStarveSequencerGossip guards against the 2026-10-25 stall of a public
// node (kb/archive/incidents/attacher_cap_starvation.md): milestones re-attached from an old past
// cone could not solidify, and while their attachers held the attacher cap the input gate dropped
// every new sequencer transaction until the memDAG TTL abandoned them.
//
// The test fills the cap the way a re-attach cascade does, straight through
// attacher.AttachTransaction and past the input gate, with milestones older than the memDAG TTL
// that can never solidify (each consumes an output of a transaction that is never stored). A
// newer milestone then arrives by gossip and must not be dropped: attachers of milestones that
// were already past the TTL when they started do not count toward the cap.
func TestPastAttachersDoNotStarveSequencerGossip(t *testing.T) {
	// hand-built sequencer milestones can't declare the attacher-computed coverageDelta
	defer reinitTestLedgerNoCoverageMonotonicity()()

	// A TTL of a few slots makes the stuck milestones past the TTL within seconds and lets their
	// attachers be abandoned within the test. The size cap stays high so the backstop stays out.
	// Deferred before the node's stop, so it is restored only after every attacher has ended.
	const ttlSlots = 3
	defer memdag.SetGCTuningForTesting(50000, ttlSlots, 12)()

	testData := initWorkflowTest(t, 1)
	defer testData.stopAndWait()
	require.NoError(t, testData.wrk.EnsureLatestBranches())

	rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
	oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))
	sourceOutput, err := oDatas[0].Parse()
	require.NoError(t, err)

	branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
	require.EqualValues(t, 1, len(branches))
	chainOut := branches[0].SequencerOutput.MustAsChainOutput()

	// Each missing producer spends the same source output with a different amount, so every
	// one has its own txid. None is stored or attached, so its output never solidifies.
	tsMissing := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	if tsMissing.IsSlotBoundary() {
		tsMissing = tsMissing.AddTicks(1)
	}
	nMissing := 0
	missingInput := func() *ledger.OutputWithID {
		nMissing++
		txBytes, err := utxodb.MakeSimpleTransferTransaction(utxodb.NewTransferData(testData.privKey, testData.addr, tsMissing).
			MustWithInputs(sourceOutput).
			WithAmount(uint64(1_000_000_000 + nMissing)).
			WithTargetLock(ledger.ChainLockFromChainID(testData.bootstrapChainID)))
		require.NoError(t, err)
		tx, err := transaction.ParseWithPartialValidation(txBytes)
		require.NoError(t, err)
		return tx.MustProducedOutputWithIDAt(1)
	}

	pace := int(ledger.L(0).TransactionPaceSequencer)

	// stuckMilestone returns a valid milestone at ts that waits forever on a missing input. One in
	// a later slot than the branch holding its chain input names that branch as explicit baseline,
	// as the ledger requires of a milestone with a cross-slot chain predecessor. Such a bootstrap
	// transaction passes the input gate, because this node's LRB is far behind its slot.
	stuckMilestone := func(ts base.LedgerTime) (base.TransactionID, []byte) {
		in := missingInput()
		ts = base.MaximumTime(ts, in.Timestamp().AddTicks(pace))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(1)
		}
		var baseline *base.TransactionID
		if ts.Slot != chainOut.ID.Slot() {
			baseline = util.Ref(chainOut.ID.TransactionID())
		}
		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "testSeq",
			Timestamp:        ts,
			ChainInput:       chainOut,
			AdditionalInputs: []*ledger.OutputWithID{in},
			ExplicitBaseline: baseline,
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       genesisPrivateKey,
			PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err)
		tx, err := transaction.ParseWithPartialValidation(txBytes)
		require.NoError(t, err)
		return tx.ID(), txBytes
	}

	// gossip delivers a milestone as an unsolicited peer transaction, through the input gate
	gossip := func(txid base.TransactionID, txBytes []byte) {
		testData.wrk.TxBytesInFromPeerQueued(txBytes, nil, testData.wrk.SelfPeerID(), txid)
	}

	tsOld := base.MaximumTime(chainOut.Timestamp(), tsMissing).AddTicks(pace)
	// the old milestones must already be past the TTL when their attachers start
	require.Eventually(t, func() bool { return tsOld.Slot+ttlSlots < ledger.TimeNow().Slot },
		10*time.Second, 50*time.Millisecond)

	// 1. The node hears a current milestone: it passes the gate and sets the gate's latest
	//    attached timestamp, the reference for "older" and "newer" below.
	tsCurrent := ledger.TimeNow()
	idCurrent, bytesCurrent := stuckMilestone(tsCurrent)
	gossip(idCurrent, bytesCurrent)
	require.Eventually(t, func() bool { return attacher.NumAttachers() >= 1 }, 5*time.Second, 10*time.Millisecond)

	// 2. A re-attach cascade fills the cap with old milestones that cannot solidify.
	capacity := testData.wrk.MaxConcurrentAttachers()
	for attacher.NumAttachers() < capacity {
		_, txBytes := stuckMilestone(tsOld)
		_, err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err)
	}
	t.Logf("attachers: %d (of them past the TTL: %d), cap: %d",
		attacher.NumAttachers(), attacher.NumPastAttachers(), capacity)
	require.GreaterOrEqual(t, attacher.NumPastAttachers(), capacity-1)

	// 3. A newer milestone arrives by gossip while the past attachers hold the cap.
	dropsBefore := testData.wrk.Counter("seq_drop")
	attachersBefore := attacher.NumAttachers()
	idNew, bytesNew := stuckMilestone(tsCurrent.AddTicks(3 * pace))
	gossip(idNew, bytesNew)
	require.Eventually(t, func() bool { return attacher.NumAttachers() > attachersBefore }, 5*time.Second, 10*time.Millisecond,
		"the newer milestone was not attached")
	require.Zero(t, testData.wrk.Counter("seq_drop")-dropsBefore,
		"a new sequencer milestone was dropped because attachers of past milestones held the cap")

	// 4. The stale-attacher guard still abandons the stuck attachers after the TTL.
	require.Eventually(t, func() bool { return attacher.NumPastAttachers() == 0 }, 30*time.Second, 50*time.Millisecond,
		"stuck attachers were not abandoned after the memDAG TTL")
}

// TestReplayedOldMilestoneDropped guards against the trigger of the same incident: a sequencer
// milestone far older than the memDAG TTL, replayed by gossip, was re-attached because the node
// still held its vertex in detached form, and the re-attach walked its old past cone starting an
// attacher for every retained milestone in it.
//
// The node runs a sequencer for a few slots with a short memDAG TTL. The test keeps a reference
// to one early milestone, standing in for whatever retained it on the node, so that the vertex
// stays in the memDAG detached. Gossiped again, the milestone must be dropped by the input gate
// and stay detached. The same milestone, once marked as pulled (a past cone asked for it), must
// still pass and be re-attached.
func TestReplayedOldMilestoneDropped(t *testing.T) {
	const (
		ttlSlots  = 3
		nBranches = 5
	)
	// deferred before the node's stop, so it is restored only after every attacher has ended
	defer memdag.SetGCTuningForTesting(50000, ttlSlots, 12)()
	testData := initWorkflowTest(t, 1, true) // memDAG GC on: the early milestone must be detached
	defer testData.stopAndWait()

	var (
		mu       sync.Mutex
		oldVid   *vertex.WrappedTx // the reference held on purpose: keeps the vertex in the memDAG
		oldBytes []byte
	)
	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(nBranches))
	require.NoError(t, err)
	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		mu.Lock()
		defer mu.Unlock()
		if oldVid != nil || ms.IsBranchTransaction() {
			return
		}
		if tx := ms.GetTransaction(); tx != nil {
			oldVid, oldBytes = ms, tx.Bytes()
		}
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
	vid, txBytes := oldVid, oldBytes
	mu.Unlock()
	require.NotNil(t, vid, "no non-branch milestone captured")

	isDetached := func() (ret bool) {
		vid.RUnwrap(vertex.UnwrapOptions{DetachedVertex: func(_ *vertex.DetachedVertex) { ret = true }})
		return
	}
	require.Eventually(t, isDetached, 30*time.Second, 50*time.Millisecond, "memDAG GC did not detach the old milestone")
	require.Eventually(t, func() bool { return vid.Slot()+ttlSlots < ledger.TimeNow().Slot }, 10*time.Second, 50*time.Millisecond)
	t.Logf("old milestone %s detached, still held in the memDAG", vid.IDShortString())

	// stayedDetached watches the vertex for a while: much shorter than the TTL, so a re-attached
	// vertex cannot be detached again in the window
	stayedDetached := func() bool {
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if !isDetached() {
				return false
			}
		}
		return true
	}

	// replay: the old milestone arrives again as unsolicited gossip
	testData.wrk.TxBytesInFromPeerQueued(txBytes, nil, testData.wrk.SelfPeerID(), vid.ID())
	require.True(t, stayedDetached(), "an unsolicited milestone older than the memDAG TTL was re-attached")
	require.EqualValues(t, 1, testData.wrk.Counter("old_seq_drop"))

	// solicited: once pulled, the same old milestone passes the gate and is re-attached
	testData.wrk.AddPulledTransaction(vid.ID())
	testData.wrk.TxBytesInFromPeerQueued(txBytes, nil, testData.wrk.SelfPeerID(), vid.ID())
	require.False(t, stayedDetached(), "a pulled old milestone was not re-attached")
	require.EqualValues(t, 1, testData.wrk.Counter("old_seq_drop"))
	runtime.KeepAlive(vid)
}
