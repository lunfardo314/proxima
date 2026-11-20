package tests

import (
	"crypto/ed25519"
	"runtime"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// staleMilestone is a hand-built milestone of a stuck chain and the chain output it produces
type staleMilestone struct {
	bytes []byte
	id    base.TransactionID
	out   ledger.OutputWithChainID
}

func makeStaleMilestone(t *testing.T, par txbuilder_seq.MakeSimpleSequencerTransactionParams) staleMilestone {
	par.SeqName = "stale"
	par.SignatureType = base.SignatureTypeED25519
	par.PublicKey = ed25519.PrivateKey(par.PrivateKey).Public().(ed25519.PublicKey)
	txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(par)
	require.NoError(t, err)
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	out := tx.SequencerOutput()
	chainOut, ok := ledger.AsOutputWithChainID(out.Output, out.ID)
	require.True(t, ok)
	return staleMilestone{bytes: txBytes, id: tx.ID(), out: chainOut}
}

// staleBootstrapTs waits until the current slot leaves room for a bootstrap transaction and nFollowers
// milestones after it, a sequencer pace apart, past the post-branch consolidation ticks and none in the
// future, and returns the bootstrap transaction's timestamp.
func staleBootstrapTs(nFollowers int) base.LedgerTime {
	pace := int(ledger.L(ledger.TimeNow().Slot).TransactionPaceSequencer)
	for {
		now := ledger.TimeNow()
		if int(now.Tick) >= 12+nFollowers*pace+2 && int(now.Tick)+2 < base.TicksPerSlot {
			return base.T(now.Slot, now.Tick-byte(nFollowers*pace)-2)
		}
		time.Sleep(ledger.TickDuration())
	}
}

// bootstrapCoverage is the coverage delta the node computes for a bootstrap transaction on baseline consuming
// only the chain output: the chain output's coverage at the transaction's timestamp (frozen coverage included)
// plus the baseline's branch inflation, since the past cone does not consume the baseline's sequencer output
func bootstrapCoverage(baseline *multistate.BranchData, chainOut *ledger.OutputWithID, ts base.LedgerTime) *uint64 {
	ret := ledger.Coverage(chainOut.Output, chainOut.ID, ts) + baseline.SequencerOutput.Output.Inflation()
	return &ret
}

// TestStaleBootstrapStreamDoesNotPinMemDAG reproduces the memDAG pin of 2026-11-19/20. A second sequencer
// runs on the network, its milestone M committed in a branch L and consumed by its next milestones in the
// branches after L. Then it gets stuck as a node stuck on L does: every slot it issues a bootstrap transaction
// on explicit baseline L, re-spending M's chain output, and an ordinary milestone after it. The bootstrap
// transaction is dropped at reception (the network is branching), but the follower passes the gate and pulls
// it from the txstore, so a young vertex holds M every slot, as on the network.
//
// With strong consumer edges, M kept everything that consumed it alive, through them every later branch, and
// so the whole memDAG from L's slot on: the records behind the TTL grew with the network's traffic. With weak
// consumer edges only M and the stuck chain's young vertices stay; the records of the main lineage behind the
// TTL are reclaimed while the stream goes on.
func TestStaleBootstrapStreamDoesNotPinMemDAG(t *testing.T) {
	const (
		ttlSlots    = 4
		streamSlots = 45
		// the window checked ends this many slots before now: past the TTLs, the GC period and the branch
		// records' retention, which in this test keep about 24 slots of the network in memory
		windowLagSlots = 30
	)
	defer memdag.SetGCTuningForTesting(50000, ttlSlots, 12)()

	td := initMultiSequencerTest(t, 1, true)
	td.env.RepeatInBackground("test GC loop", time.Second, func() bool {
		runtime.GC()
		return true
	})
	td.startSequencersWithTimeout(1000)
	stuckID := td.chainOrigins[0].ChainID

	// L: a reliable branch committing a milestone M of the second sequencer (not its chain origin)
	var baseline *multistate.BranchData
	var chainOut *ledger.OutputWithID
	deadline := time.Now().Add(30 * ledger.SlotDuration())
	for {
		lrb := td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil && lrb.Stem.ID.Slot() > td.distributionBranchTxID.Slot() {
			if o, err := td.wrk.Branches().GetChainOutputFromBranch(lrb.Stem.ID.TransactionID(), stuckID); err == nil && o.ID.IsSequencerTransaction() {
				baseline, chainOut = lrb, o
				break
			}
		}
		require.True(t, time.Now().Before(deadline), "no reliable branch commits a milestone of the second sequencer")
		time.Sleep(ledger.TickDuration())
	}
	baselineID := baseline.Stem.ID.TransactionID()
	// the network moves on: the second sequencer's next milestones consume M's chain output in the main lineage
	deadline = time.Now().Add(30 * ledger.SlotDuration())
	for {
		lrb := td.wrk.Branches().FindLatestReliableBranch()
		if o, err := td.wrk.Branches().GetChainOutputFromBranch(lrb.Stem.ID.TransactionID(), stuckID); err == nil && o.ID != chainOut.ID {
			break
		}
		require.True(t, time.Now().Before(deadline), "the second sequencer did not move on from M")
		time.Sleep(ledger.TickDuration())
	}
	td.sequencers[0].Stop()
	t.Logf("baseline L %s, M %s; the second sequencer stopped at slot %d", baselineID.StringShort(), chainOut.ID.StringShort(), ledger.TimeNow().Slot)

	chainIn, ok := ledger.AsOutputWithChainID(chainOut.Output, chainOut.ID)
	require.True(t, ok)
	pace := int(ledger.L(ledger.TimeNow().Slot).TransactionPaceSequencer)

	// records in the memDAG, detached ones included, whose transactions are in the given slots: those of the
	// network, and those of the stuck chain's stream, which its young vertices hold for a while
	streamIDs := make(map[base.TransactionID]struct{})
	countRecords := func(from, to uint32) (network, stream int) {
		for vid := range td.wrk.VerticesWithExpirationFlag() {
			if s := vid.Slot(); s >= from && s <= to {
				if _, isStream := streamIDs[vid.ID()]; isStream {
					stream++
				} else {
					network++
				}
			}
		}
		return
	}
	attached := 0
	var oldRecords, oldStream int
	for i := 0; i < streamSlots; i++ {
		ts := staleBootstrapTs(1)
		bootstrap := makeStaleMilestone(t, txbuilder_seq.MakeSimpleSequencerTransactionParams{
			Timestamp:             ts,
			ChainInput:            &chainIn,
			ExplicitBaseline:      &baselineID,
			DoNotInflateMainChain: true,
			CoverageDelta:         bootstrapCoverage(baseline, chainOut, ts),
			PrivateKey:            td.privKeyAux,
		})
		follower := makeStaleMilestone(t, txbuilder_seq.MakeSimpleSequencerTransactionParams{
			Timestamp:             ts.AddTicks(pace),
			ChainInput:            &bootstrap.out,
			DoNotInflateMainChain: true,
			PrivateKey:            td.privKeyAux,
		})
		streamIDs[bootstrap.id] = struct{}{}
		streamIDs[follower.id] = struct{}{}
		td.wrk.TxBytesInFromPeerQueued(bootstrap.bytes, nil, peer.ID("some peer"), bootstrap.id)
		td.wrk.TxBytesInFromPeerQueued(follower.bytes, nil, peer.ID("some peer"), follower.id)

		// the follower pulls the dropped bootstrap transaction and it attaches: a young vertex holds M
		time.Sleep(ledger.SlotDuration() / 2)
		if vid := td.wrk.GetVertex(bootstrap.id); vid != nil && vid.KindString() == "vertex" {
			attached++
		}
		for ledger.TimeNow().Slot == ts.Slot {
			time.Sleep(ledger.TickDuration())
		}
		nowSlot := ledger.TimeNow().Slot
		lrb := td.wrk.Branches().FindLatestReliableBranch()
		require.False(t, global.NetworkStuckAt(lrb.Stem.ID.Slot(), nowSlot), "the network must keep branching")
		// the window from the slot after L, which with M the stuck chain holds legitimately, to well behind now
		if to := nowSlot - windowLagSlots; to > baselineID.Slot() {
			oldRecords, oldStream = countRecords(baselineID.Slot()+1, to)
			t.Logf("slot %d: memDAG %d records; in slots %d..%d: %d of the network, %d of the stuck chain; bootstrap transactions attached %d/%d",
				nowSlot, td.wrk.NumVertices(), baselineID.Slot()+1, to, oldRecords, oldStream, attached, i+1)
		}
	}
	require.Greater(t, attached, streamSlots/2, "the stuck chain's bootstrap transactions must be pulled in and attached, as on the network")
	require.Zero(t, oldRecords+oldStream, "the stuck chain must not keep the memDAG behind its baseline")

	td.stop()
	td.waitStop()
}

// TestStaleBootstrapLineageReloadedForBranch: a valid bootstrap transaction and a follower of a chain on an
// older branch L attach, and are detached by the memDAG once past the TTL. Then a branch of that chain on L
// arrives, built over the two: the node gets both back and all three end Good. Pruning a stuck lineage never
// costs a branch that needs it.
func TestStaleBootstrapLineageReloadedForBranch(t *testing.T) {
	defer memdag.SetGCTuningForTesting(50000, 2, 12)()

	td := initWorkflowTest(t, 1, true)
	td.env.RepeatInBackground("test GC loop", time.Second, func() bool {
		runtime.GC()
		return true
	})
	// half the auxiliary output funds the chain, the rest stays with the key as change
	td.makeChainOriginsWithAmounts([]uint64{initBalance / 2})
	require.NoError(t, td.attachChainOriginTxs())
	chainID := td.chainOrigins[0].ChainID

	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey, sequencer.WithMaxBranches(60))
	require.NoError(t, err)
	seq.OnExitOnce(func() {
		td.stop()
	})
	seq.Start()

	// L: a branch of a branching network that commits the chain origin
	var baseline *multistate.BranchData
	var chainOut *ledger.OutputWithID
	deadline := time.Now().Add(10 * ledger.SlotDuration())
	for {
		baseline = td.wrk.Branches().FindLatestReliableBranch()
		if baseline != nil && !global.NetworkStuckAt(baseline.Stem.ID.Slot(), ledger.TimeNow().Slot) {
			if o, err := td.wrk.Branches().GetChainOutputFromBranch(baseline.Stem.ID.TransactionID(), chainID); err == nil {
				chainOut = o
				break
			}
		}
		require.True(t, time.Now().Before(deadline), "the chain origin was not committed")
		time.Sleep(ledger.TickDuration())
	}
	baselineID := baseline.Stem.ID.TransactionID()
	for ledger.TimeNow().Slot <= baselineID.Slot() {
		time.Sleep(ledger.TickDuration())
	}
	rdr := multistate.MakeSugared(td.wrk.Branches().GetStateReaderForTheBranch(baselineID))
	auxOuts, err := rdr.GetOutputsForAccount(td.addrAux.ControllerID())
	require.NoError(t, err)
	// the key holds the chain output and the change of the chain origin transaction
	spare := util.PurgeSlice(auxOuts, func(o *ledger.OutputWithID) bool { return o.ID != chainOut.ID })
	require.EqualValues(t, 1, len(spare))
	chainIn, ok := ledger.AsOutputWithChainID(chainOut.Output, chainOut.ID)
	require.True(t, ok)

	// each declares the coverage delta the node computes; the follower also consumes the spare output, so its
	// coverage strictly increases
	pace := int(ledger.L(ledger.TimeNow().Slot).TransactionPaceSequencer)
	ts := staleBootstrapTs(1)
	withSpare := func(ts base.LedgerTime) *uint64 {
		ret := *bootstrapCoverage(baseline, chainOut, ts) + ledger.Coverage(spare[0].Output, spare[0].ID, ts)
		return &ret
	}
	bootstrap := makeStaleMilestone(t, txbuilder_seq.MakeSimpleSequencerTransactionParams{
		Timestamp:             ts,
		ChainInput:            &chainIn,
		ExplicitBaseline:      &baselineID,
		DoNotInflateMainChain: true,
		CoverageDelta:         bootstrapCoverage(baseline, chainOut, ts),
		PrivateKey:            td.privKeyAux,
	})
	follower := makeStaleMilestone(t, txbuilder_seq.MakeSimpleSequencerTransactionParams{
		Timestamp:             ts.AddTicks(pace),
		ChainInput:            &bootstrap.out,
		AdditionalInputs:      spare,
		DoNotInflateMainChain: true,
		CoverageDelta:         withSpare(ts.AddTicks(pace)),
		PrivateKey:            td.privKeyAux,
	})
	td.wrk.TxBytesInFromPeerQueued(bootstrap.bytes, nil, peer.ID("some peer"), bootstrap.id)
	td.wrk.TxBytesInFromPeerQueued(follower.bytes, nil, peer.ID("some peer"), follower.id)

	isFull := func(txid base.TransactionID) bool {
		vid := td.wrk.GetVertex(txid)
		return vid != nil && vid.KindString() == "vertex"
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if vid := td.wrk.GetVertex(follower.id); vid != nil && vid.GetTxStatus() != vertex.Undefined {
			require.Equal(t, vertex.Good, vid.GetTxStatus(), "the follower: %v", vid.GetError())
			break
		}
		require.True(t, time.Now().Before(deadline), "the follower did not attach")
		time.Sleep(ledger.TickDuration())
	}
	require.Equal(t, vertex.Good, td.wrk.GetVertex(bootstrap.id).GetTxStatus(), "the bootstrap transaction pulled for the follower attaches")

	// the memDAG lets go of both
	deadline = time.Now().Add(20 * ledger.SlotDuration())
	for isFull(bootstrap.id) || isFull(follower.id) {
		require.True(t, time.Now().Before(deadline), "the stale lineage was not detached")
		time.Sleep(ledger.SlotDuration())
	}
	t.Logf("detached at slot %d: bootstrap transaction in the memDAG %v, follower %v", ledger.TimeNow().Slot,
		td.wrk.GetVertex(bootstrap.id) != nil, td.wrk.GetVertex(follower.id) != nil)

	// the branch of the stale lineage in the next slot, with the stem values of its past cone: the bootstrap
	// transaction and the follower, no inflation, the coverage of the two baseline outputs they consume
	branchTs := base.T(ledger.TimeNow().Slot+1, 0)
	branch := makeStaleMilestone(t, txbuilder_seq.MakeSimpleSequencerTransactionParams{
		ChainInput: &follower.out,
		StemInput:  baseline.Stem,
		Timestamp:  branchTs,
		PrivateKey: td.privKeyAux,
		StemAggregates: &txbuilder_seq.StemAggregates{
			CoverageDelta:            *withSpare(branchTs),
			BaselineFrozenCoverage:   baseline.FrozenCoverage,
			NumConfirmedTransactions: 2,
			NumSeqTransactions:       2,
			NumSeq:                   1,
			BaselineRoot:             baseline.Root.Bytes(),
		},
	})
	for ledger.TimeNow().Before(branchTs) {
		time.Sleep(ledger.TickDuration())
	}
	td.wrk.TxBytesInFromPeerQueued(branch.bytes, nil, peer.ID("some peer"), branch.id)

	deadline = time.Now().Add(15 * time.Second)
	for {
		br := td.wrk.GetVertex(branch.id)
		if br != nil && br.GetTxStatus() != vertex.Undefined {
			require.Equal(t, vertex.Good, br.GetTxStatus(), "the branch must attach Good: %v", br.GetError())
			break
		}
		require.True(t, time.Now().Before(deadline), "the branch did not attach")
		time.Sleep(ledger.TickDuration())
	}
	for _, txid := range []base.TransactionID{bootstrap.id, follower.id} {
		vid := td.wrk.GetVertex(txid)
		require.NotNil(t, vid)
		require.Equal(t, vertex.Good, vid.GetTxStatus(), "%s: %v", txid.StringShort(), vid.GetError())
	}

	td.waitStop()
}
