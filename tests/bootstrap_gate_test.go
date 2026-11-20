package tests

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
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

// The reception gate on bootstrap transactions mirrors the issuing condition: an unsolicited
// bootstrap transaction (explicit baseline) is attached only when this node's own latest
// reliable branch also lags the transaction's slot by the bootstrap lag, i.e. this node sees the
// network as stuck too. Otherwise the sender's LRB is frozen while the network is branching, and
// the transaction is ignored (never invalidated). The two tests below gossip a hand-built
// bootstrap transaction into a workflow in each of the two states.

// gossipBootstrapTx builds a bootstrap transaction (makeBootstrapTx) and feeds it to the workflow as if
// received from a peer. It returns the transaction ID.
func gossipBootstrapTx(t *testing.T, td *workflowTestData, baseline *multistate.BranchData, ts func(base.LedgerTime) base.LedgerTime) base.TransactionID {
	txBytes, txid := makeBootstrapTx(t, td, baseline, ts)
	td.wrk.TxBytesInFromPeerQueued(txBytes, nil, peer.ID("some peer"), txid)
	return txid
}

// makeBootstrapTx builds a bootstrap transaction of the bootstrap sequencer anchored on the given
// branch — the chain output as committed in that branch, the branch as explicit baseline, no
// endorsements — timestamped by ts from the chain output's timestamp. The caller makes sure the
// branch is in a past slot, as the ledger requires of an explicit baseline.
func makeBootstrapTx(t *testing.T, td *workflowTestData, baseline *multistate.BranchData, ts func(base.LedgerTime) base.LedgerTime) ([]byte, base.TransactionID) {
	lrbID := baseline.Stem.ID.TransactionID()
	require.Less(t, lrbID.Slot(), ledger.TimeNow().Slot)

	chainOut, err := td.wrk.Branches().GetChainOutputFromBranch(lrbID, td.bootstrapChainID)
	require.NoError(t, err)
	chainIn, ok := ledger.AsOutputWithChainID(chainOut.Output, chainOut.ID)
	require.True(t, ok)

	txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:          "stale",
		Timestamp:        ts(chainOut.Timestamp()),
		ChainInput:       &chainIn,
		ExplicitBaseline: &lrbID,
		SignatureType:    base.SignatureTypeED25519,
		PrivateKey:       genesisPrivateKey,
		PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
	})
	require.NoError(t, err)
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	_, isBootstrap := tx.ExplicitBaseline()
	require.True(t, isBootstrap)
	return txBytes, tx.ID()
}

// pastTimestamp returns a ledger time a couple of ticks in the past of the current slot, so the
// transaction is never deferred by clock alignment, and at least the sequencer pace after the
// chain input. It waits for the slot to be old enough rather than stepping back into the previous
// slot, which the gate would judge differently.
func pastTimestamp(chainInputTs base.LedgerTime) base.LedgerTime {
	for {
		now := ledger.TimeNow()
		if now.Tick >= 4 {
			ts := base.T(now.Slot, now.Tick-2)
			if ledger.ValidSequencerPace(chainInputTs, ts) {
				return ts
			}
		}
		time.Sleep(ledger.TickDuration())
	}
}

// TestBootstrapTxIgnoredWhileBranching: a sequencer keeps the node's LRB current, so a gossiped
// bootstrap transaction anchored on that LRB is dropped by the reception gate and never reaches
// the memDAG.
func TestBootstrapTxIgnoredWhileBranching(t *testing.T) {
	td := initWorkflowTest(t, 1)

	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey, sequencer.WithMaxBranches(8))
	require.NoError(t, err)
	seq.OnExitOnce(func() {
		td.stop()
	})
	seq.Start()

	// wait until branches are being produced: the LRB is within the bootstrap lag of now
	deadline := time.Now().Add(8 * ledger.SlotDuration())
	var lrb *multistate.BranchData
	for {
		lrb = td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil && !global.NetworkStuckAt(lrb.Stem.ID.Slot(), ledger.TimeNow().Slot) &&
			lrb.Stem.ID.Slot() > td.distributionBranchTxID.Slot() {
			break
		}
		require.True(t, time.Now().Before(deadline), "the sequencer did not bring the LRB up to date")
		time.Sleep(ledger.TickDuration())
	}
	// the explicit baseline must be in a past slot: anchor on this LRB from the next slot on.
	// The LRB at reception is then this branch or a newer one, either way within the lag.
	for ledger.TimeNow().Slot <= lrb.Stem.ID.Slot() {
		time.Sleep(ledger.TickDuration())
	}

	txid := gossipBootstrapTx(t, td, lrb, pastTimestamp)
	time.Sleep(ledger.SlotDuration())

	require.EqualValues(t, 1, td.wrk.Counter("bootstrap_drop"))
	require.Nil(t, td.wrk.GetVertex(txid), "an ignored bootstrap transaction must not be attached")
	// ignored is not invalidated: the transaction bytes are kept, so it can still be pulled
	// if some branch's past cone turns out to need it
	require.True(t, td.wrk.TxBytesStore().HasTxBytes(&txid))

	td.waitStop()
}

// TestBootstrapTxFromClockAheadSenderIgnored: the network is branching, and the bootstrap
// transaction is timestamped the bootstrap lag (BootstrapLRBLagSlots) into the future, as a
// sender whose clock runs ahead would send it. Judged at reception the node's LRB, which is never
// ahead of the clock, lags the transaction's slot by the bootstrap lag, so the gate passes and the
// transaction waits for its ledger time; by then the LRB has moved up and the gate, judged again,
// drops it. Two slots ahead was not enough: when the sequencer committed the current slot's branch
// before the transaction arrived, the gate dropped it at reception.
func TestBootstrapTxFromClockAheadSenderIgnored(t *testing.T) {
	td := initWorkflowTest(t, 1)

	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey, sequencer.WithMaxBranches(12))
	require.NoError(t, err)
	seq.OnExitOnce(func() {
		td.stop()
	})
	seq.Start()

	deadline := time.Now().Add(8 * ledger.SlotDuration())
	var lrb *multistate.BranchData
	for {
		lrb = td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil && !global.NetworkStuckAt(lrb.Stem.ID.Slot(), ledger.TimeNow().Slot) &&
			lrb.Stem.ID.Slot() > td.distributionBranchTxID.Slot() {
			break
		}
		require.True(t, time.Now().Before(deadline), "the sequencer did not bring the LRB up to date")
		time.Sleep(ledger.TickDuration())
	}
	for ledger.TimeNow().Slot <= lrb.Stem.ID.Slot() {
		time.Sleep(ledger.TickDuration())
	}

	const slotsAhead = global.BootstrapLRBLagSlots
	txid := gossipBootstrapTx(t, td, lrb, func(chainInputTs base.LedgerTime) base.LedgerTime {
		// a non-zero tick: tick 0 is a branch timestamp
		return base.T(ledger.TimeNow().Slot+slotsAhead, 10)
	})
	// at reception: not dropped, waiting for its ledger time
	time.Sleep(ledger.SlotDuration() / 2)
	require.EqualValues(t, 0, td.wrk.Counter("bootstrap_drop"))
	require.Nil(t, td.wrk.GetVertex(txid))

	time.Sleep((slotsAhead + 1) * ledger.SlotDuration())
	require.EqualValues(t, 1, td.wrk.Counter("bootstrap_drop"))
	require.Nil(t, td.wrk.GetVertex(txid), "a bootstrap transaction from a clock-ahead sender must not be attached once its slot comes")

	td.waitStop()
}

// TestBootstrapTxAttachedWhileStuck: no sequencer runs, the LRB stays at the distribution branch
// and falls behind by more than the bootstrap lag. A gossiped bootstrap transaction is then in
// order and attaches.
func TestBootstrapTxAttachedWhileStuck(t *testing.T) {
	td := initWorkflowTest(t, 1)

	lrb := td.wrk.Branches().FindLatestReliableBranch()
	require.NotNil(t, lrb)
	// let the LRB fall behind by the bootstrap lag
	for !global.NetworkStuckAt(lrb.Stem.ID.Slot(), ledger.TimeNow().Slot) {
		time.Sleep(ledger.TickDuration())
	}

	txid := gossipBootstrapTx(t, td, lrb, pastTimestamp)
	time.Sleep(ledger.SlotDuration())

	require.EqualValues(t, 0, td.wrk.Counter("bootstrap_drop"))
	require.NotNil(t, td.wrk.GetVertex(txid), "a bootstrap transaction in a stuck network must be attached")

	td.stop()
	td.waitStop()
}

// waitBranchingLRB starts the bootstrap sequencer and waits until its branches keep the LRB within the
// bootstrap lag of now, then until the slot after that LRB, so the LRB can serve as an explicit baseline.
func waitBranchingLRB(t *testing.T, td *workflowTestData) *multistate.BranchData {
	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey, sequencer.WithMaxBranches(12))
	require.NoError(t, err)
	seq.OnExitOnce(func() {
		td.stop()
	})
	seq.Start()

	deadline := time.Now().Add(8 * ledger.SlotDuration())
	var lrb *multistate.BranchData
	for {
		lrb = td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil && !global.NetworkStuckAt(lrb.Stem.ID.Slot(), ledger.TimeNow().Slot) &&
			lrb.Stem.ID.Slot() > td.distributionBranchTxID.Slot() {
			break
		}
		require.True(t, time.Now().Before(deadline), "the sequencer did not bring the LRB up to date")
		time.Sleep(ledger.TickDuration())
	}
	for ledger.TimeNow().Slot <= lrb.Stem.ID.Slot() {
		time.Sleep(ledger.TickDuration())
	}
	return lrb
}

// TestBootstrapTxPulledIgnoredWhileBranching: the network is branching and a milestone outside any branch
// needs a bootstrap transaction, as a stuck sequencer's next milestone needs its bootstrap transaction. The
// copy loaded from the txstore for it is not attached either: the dependency stays virtual and nothing is
// marked BAD. Before, this path attached it, and with it the stuck chain's old output stayed referenced.
func TestBootstrapTxPulledIgnoredWhileBranching(t *testing.T) {
	td := initWorkflowTest(t, 1)
	lrb := waitBranchingLRB(t, td)

	txBytes, txid := makeBootstrapTx(t, td, lrb, pastTimestamp)
	// the dependency as a milestone's attacher holds it before the bytes arrive
	vid := attacher.AttachTxID(txid, td.wrk)
	td.wrk.TxBytesFromStoreInSolicited(txBytes)
	time.Sleep(ledger.SlotDuration())

	require.EqualValues(t, 1, td.wrk.Counter("bootstrap_pulled_drop"))
	require.True(t, vid.IsVirtualTx(), "a pulled stale bootstrap transaction no branch needs must not be attached")
	require.NotEqual(t, "BAD", vid.GetTxStatus().String())

	td.waitStop()
}

// TestBootstrapTxPulledForBranchAttached: the same, but a branch being attached needs the bootstrap
// transaction (its attacher marked the dependency). The copy loaded from the txstore is attached: a branch
// can still commit a bootstrap transaction, as the ingress gate's design promises.
func TestBootstrapTxPulledForBranchAttached(t *testing.T) {
	td := initWorkflowTest(t, 1)
	lrb := waitBranchingLRB(t, td)

	txBytes, txid := makeBootstrapTx(t, td, lrb, pastTimestamp)
	vid := attacher.AttachTxID(txid, td.wrk)
	vid.MarkNeededByBranch()
	td.wrk.TxBytesFromStoreInSolicited(txBytes)
	time.Sleep(ledger.SlotDuration())

	require.EqualValues(t, 0, td.wrk.Counter("bootstrap_pulled_drop"))
	require.False(t, vid.IsVirtualTx(), "a bootstrap transaction a branch needs must be attached")

	td.waitStop()
}

// staleChain is a stuck chain as observed on 2026-11-20: a bootstrap transaction on the given baseline and two
// ordinary milestones after it, each consuming its predecessor's chain output, with no explicit baseline and no
// endorsements, so they pass the reception gate as a stuck sequencer's next milestones did.
type staleChain struct {
	bootstrapBytes, follower1Bytes, follower2Bytes []byte
	bootstrapID, follower1ID, follower2ID          base.TransactionID
	follower2Out                                   ledger.OutputWithChainID
}

func makeStaleChain(t *testing.T, td *workflowTestData, baseline *multistate.BranchData) *staleChain {
	pace := int(ledger.L(ledger.TimeNow().Slot).TransactionPaceSequencer)
	// all three after the post-branch consolidation ticks, a pace apart, none in the future
	var bootstrapTs base.LedgerTime
	for {
		now := ledger.TimeNow()
		if int(now.Tick) >= 12+2*pace+2 && int(now.Tick)+2 < base.TicksPerSlot {
			bootstrapTs = base.T(now.Slot, now.Tick-byte(2*pace)-2)
			break
		}
		time.Sleep(ledger.TickDuration())
	}
	ret := &staleChain{}
	ret.bootstrapBytes, ret.bootstrapID = makeBootstrapTx(t, td, baseline, func(base.LedgerTime) base.LedgerTime { return bootstrapTs })

	next := func(predBytes []byte, ts base.LedgerTime) ([]byte, base.TransactionID, ledger.OutputWithChainID) {
		pred, err := transaction.ParseWithPartialValidation(predBytes)
		require.NoError(t, err)
		seqOut := pred.SequencerOutput()
		chainIn, ok := ledger.AsOutputWithChainID(seqOut.Output, seqOut.ID)
		require.True(t, ok)
		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:       "stale",
			Timestamp:     ts,
			ChainInput:    &chainIn,
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    genesisPrivateKey,
			PublicKey:     genesisPrivateKey.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err)
		tx, err := transaction.ParseWithPartialValidation(txBytes)
		require.NoError(t, err)
		out := tx.SequencerOutput()
		chainOut, ok := ledger.AsOutputWithChainID(out.Output, out.ID)
		require.True(t, ok)
		return txBytes, tx.ID(), chainOut
	}
	ret.follower1Bytes, ret.follower1ID, _ = next(ret.bootstrapBytes, bootstrapTs.AddTicks(pace))
	ret.follower2Bytes, ret.follower2ID, ret.follower2Out = next(ret.follower1Bytes, bootstrapTs.AddTicks(2*pace))
	return ret
}

// gossip feeds the bootstrap transaction and then its followers to the workflow as if received from a peer
func (c *staleChain) gossip(td *workflowTestData) {
	td.wrk.TxBytesInFromPeerQueued(c.bootstrapBytes, nil, peer.ID("some peer"), c.bootstrapID)
	td.wrk.TxBytesInFromPeerQueued(c.follower1Bytes, nil, peer.ID("some peer"), c.follower1ID)
	td.wrk.TxBytesInFromPeerQueued(c.follower2Bytes, nil, peer.ID("some peer"), c.follower2ID)
}

// waitAbandoned waits until the milestone's attachment has ended, and requires that it ended without a verdict
func waitAbandoned(t *testing.T, td *workflowTestData, txid base.TransactionID, within time.Duration) *vertex.WrappedTx {
	deadline := time.Now().Add(within)
	for {
		if vid := td.wrk.GetVertex(txid); vid != nil && vid.FlagsUp(vertex.FlagVertexTxAttachmentFinished) {
			require.Equal(t, vertex.Undefined, vid.GetTxStatus(), "%s must end without a verdict: %v", txid.StringShort(), vid.GetError())
			return vid
		}
		require.True(t, time.Now().Before(deadline), "%s must end well before the 60 s pull deadline", txid.StringShort())
		time.Sleep(ledger.TickDuration())
	}
}

// TestBootstrapFollowersEndWithoutVerdict: the network is branching and a stuck chain sends its bootstrap
// transaction and two ordinary milestones after it. The bootstrap transaction is dropped at reception, its copy
// loaded from the txstore for the first follower is refused, and the pull expires at once. Both followers end
// within a few seconds, not after the 60 s pull deadline, and without a verdict: nothing is BAD, so a branch that
// turns out to need them can still have them. Nothing is attached, so the stuck chain's old output stays
// unreferenced.
func TestBootstrapFollowersEndWithoutVerdict(t *testing.T) {
	td := initWorkflowTest(t, 1)
	lrb := waitBranchingLRB(t, td)

	c := makeStaleChain(t, td, lrb)
	start := time.Now()
	c.gossip(td)

	follower1 := waitAbandoned(t, td, c.follower1ID, 15*time.Second)
	follower2 := waitAbandoned(t, td, c.follower2ID, 15*time.Second)
	t.Logf("both followers ended without a verdict after %v", time.Since(start))
	require.True(t, follower1.Refused())
	require.True(t, follower2.Refused())

	require.EqualValues(t, 1, td.wrk.Counter("bootstrap_drop"))
	require.GreaterOrEqual(t, td.wrk.Counter("bootstrap_pulled_drop"), 1)
	require.GreaterOrEqual(t, td.wrk.Counter("refused_dep_abort"), 2)
	bootstrap := td.wrk.GetVertex(c.bootstrapID)
	require.NotNil(t, bootstrap)
	require.True(t, bootstrap.IsVirtualTx(), "the bootstrap transaction must not be attached")
	require.NotEqual(t, vertex.Bad, bootstrap.GetTxStatus())

	td.waitStop()
}

// TestBootstrapFollowersRevivedForBranch: as above, the followers end without a verdict. Then a branch of that
// stuck lineage arrives: its chain predecessor is the second follower and its stem is the baseline's, as when a
// partition heals in that lineage's favour. The branch's attacher marks its past cone as needed by a branch and
// revives both followers, and the bootstrap transaction is attached after all, Good: the refusal does not block a
// lineage a branch needs. The followers then reach a verdict on their own content. Hand-built followers that only
// consume their predecessor cannot declare a valid coverage delta (it must strictly increase within the slot, and
// nothing new is covered), so their verdict is BAD here, for that reason and not for the refusal; the branch
// built over them is not asserted either.
func TestBootstrapFollowersRevivedForBranch(t *testing.T) {
	td := initWorkflowTest(t, 1)
	lrb := waitBranchingLRB(t, td)

	c := makeStaleChain(t, td, lrb)
	c.gossip(td)
	waitAbandoned(t, td, c.follower1ID, 15*time.Second)
	waitAbandoned(t, td, c.follower2ID, 15*time.Second)

	branchTs := c.follower2Out.ID.Timestamp().NextSlotBoundary()
	for ledger.TimeNow().Before(branchTs) {
		time.Sleep(ledger.TickDuration())
	}
	branchBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:       "stale",
		ChainInput:    &c.follower2Out,
		StemInput:     lrb.Stem,
		Timestamp:     branchTs,
		SignatureType: base.SignatureTypeED25519,
		PrivateKey:    genesisPrivateKey,
		PublicKey:     genesisPrivateKey.Public().(ed25519.PublicKey),
	})
	require.NoError(t, err)
	branchTx, err := transaction.ParseWithPartialValidation(branchBytes)
	require.NoError(t, err)
	td.wrk.TxBytesInFromPeerQueued(branchBytes, nil, peer.ID("some peer"), branchTx.ID())

	deadline := time.Now().Add(15 * time.Second)
	for {
		f1, f2, b := td.wrk.GetVertex(c.follower1ID), td.wrk.GetVertex(c.follower2ID), td.wrk.GetVertex(c.bootstrapID)
		if f1 != nil && f2 != nil && b != nil && b.GetTxStatus() == vertex.Good &&
			f1.GetTxStatus() != vertex.Undefined && f2.GetTxStatus() != vertex.Undefined {
			for _, f := range []*vertex.WrappedTx{f1, f2} {
				t.Logf("follower %s: %s %v", f.IDShortString(), f.GetTxStatus().String(), f.GetError())
				require.NotErrorIs(t, f.GetError(), attacher.ErrSolidificationDeadline)
				require.False(t, f.Refused(), "a follower a branch needs is not refused")
			}
			break
		}
		require.True(t, time.Now().Before(deadline), "a branch must get the refused bootstrap transaction attached and its followers judged")
		time.Sleep(ledger.TickDuration())
	}
	branch := td.wrk.GetVertex(branchTx.ID())
	require.NotNil(t, branch)
	t.Logf("branch %s: %s %v", branch.IDShortString(), branch.GetTxStatus().String(), branch.GetError())

	td.waitStop()
}

// TestBootstrapLineageAttachesForBranch: the end-to-end case of the refusal, with valid transactions only. A second
// chain, controlled by the auxiliary key, is committed in a branch L together with a spare output of that key. On
// L it issues a bootstrap transaction and a follower: the follower consumes the bootstrap transaction's chain output
// and the spare output, so its coverage strictly increases, and neither inflates, so each declares exactly the
// coverage delta the node computes. While the network is branching both are refused and the follower ends without
// a verdict. Then a branch of that chain on L arrives, built over the two with their stem values: the branch's
// attacher revives the follower and gets the bootstrap transaction attached, and all three end Good. The refusal
// never blocks a valid lineage a branch needs.
func TestBootstrapLineageAttachesForBranch(t *testing.T) {
	td := initWorkflowTest(t, 1)
	// half the auxiliary output funds the chain, the rest stays with the key as change
	td.makeChainOriginsWithAmounts([]uint64{initBalance / 2})
	require.NoError(t, td.attachChainOriginTxs())
	chainID := td.chainOrigins[0].ChainID

	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey, sequencer.WithMaxBranches(16))
	require.NoError(t, err)
	seq.OnExitOnce(func() {
		td.stop()
	})
	seq.Start()

	// L: a branch of a branching network that commits the chain origin
	var lrb *multistate.BranchData
	var chainOut *ledger.OutputWithID
	deadline := time.Now().Add(10 * ledger.SlotDuration())
	for {
		lrb = td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil && !global.NetworkStuckAt(lrb.Stem.ID.Slot(), ledger.TimeNow().Slot) {
			if o, err := td.wrk.Branches().GetChainOutputFromBranch(lrb.Stem.ID.TransactionID(), chainID); err == nil {
				chainOut = o
				break
			}
		}
		require.True(t, time.Now().Before(deadline), "the chain origin was not committed")
		time.Sleep(ledger.TickDuration())
	}
	lrbID := lrb.Stem.ID.TransactionID()
	for ledger.TimeNow().Slot <= lrbID.Slot() {
		time.Sleep(ledger.TickDuration())
	}
	rdr := multistate.MakeSugared(td.wrk.Branches().GetStateReaderForTheBranch(lrbID))
	auxOuts, err := rdr.GetOutputsForAccount(td.addrAux.ControllerID())
	require.NoError(t, err)
	// the key holds the chain output and the change of the chain origin transaction
	spare := util.PurgeSlice(auxOuts, func(o *ledger.OutputWithID) bool { return o.ID != chainOut.ID })
	require.EqualValues(t, 1, len(spare))
	chainIn, ok := ledger.AsOutputWithChainID(chainOut.Output, chainOut.ID)
	require.True(t, ok)

	pace := int(ledger.L(ledger.TimeNow().Slot).TransactionPaceSequencer)
	var bootstrapTs base.LedgerTime
	for {
		now := ledger.TimeNow()
		if int(now.Tick) >= 12+pace+2 && int(now.Tick)+2 < base.TicksPerSlot {
			bootstrapTs = base.T(now.Slot, now.Tick-byte(pace)-2)
			break
		}
		time.Sleep(ledger.TickDuration())
	}
	auxPub := td.privKeyAux.Public().(ed25519.PublicKey)
	parse := func(txBytes []byte) (*transaction.Transaction, ledger.OutputWithChainID) {
		tx, err := transaction.ParseWithPartialValidation(txBytes)
		require.NoError(t, err)
		out := tx.SequencerOutput()
		chainOut, ok := ledger.AsOutputWithChainID(out.Output, out.ID)
		require.True(t, ok)
		return tx, chainOut
	}
	// each declares the coverage delta the node computes: the coverage of the baseline outputs consumed in its
	// past cone, as seen at its own timestamp (a chain output's coverage includes its frozen coverage), plus the
	// baseline's branch inflation, since the cone does not consume the baseline's sequencer output
	coverage := func(ts base.LedgerTime, withSpare bool) *uint64 {
		ret := ledger.Coverage(chainOut.Output, chainOut.ID, ts) + lrb.SequencerOutput.Output.Inflation()
		if withSpare {
			ret += ledger.Coverage(spare[0].Output, spare[0].ID, ts)
		}
		return &ret
	}
	bootstrapBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:               "stale",
		Timestamp:             bootstrapTs,
		ChainInput:            &chainIn,
		ExplicitBaseline:      &lrbID,
		DoNotInflateMainChain: true,
		CoverageDelta:         coverage(bootstrapTs, false),
		SignatureType:         base.SignatureTypeED25519,
		PrivateKey:            td.privKeyAux,
		PublicKey:             auxPub,
	})
	require.NoError(t, err)
	bootstrapTx, bootstrapOut := parse(bootstrapBytes)
	followerBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:               "stale",
		Timestamp:             bootstrapTs.AddTicks(pace),
		ChainInput:            &bootstrapOut,
		AdditionalInputs:      spare,
		DoNotInflateMainChain: true,
		CoverageDelta:         coverage(bootstrapTs.AddTicks(pace), true),
		SignatureType:         base.SignatureTypeED25519,
		PrivateKey:            td.privKeyAux,
		PublicKey:             auxPub,
	})
	require.NoError(t, err)
	followerTx, followerOut := parse(followerBytes)

	td.wrk.TxBytesInFromPeerQueued(bootstrapBytes, nil, peer.ID("some peer"), bootstrapTx.ID())
	td.wrk.TxBytesInFromPeerQueued(followerBytes, nil, peer.ID("some peer"), followerTx.ID())
	waitAbandoned(t, td, followerTx.ID(), 15*time.Second)
	require.True(t, td.wrk.GetVertex(bootstrapTx.ID()).IsVirtualTx())

	// the branch of the stale lineage, with the stem values of its past cone: the bootstrap transaction and the
	// follower, no inflation, the coverage of the two baseline outputs they consume, one sequencer
	branchTs := followerOut.ID.Timestamp().NextSlotBoundary()
	for ledger.TimeNow().Before(branchTs) {
		time.Sleep(ledger.TickDuration())
	}
	branchBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:       "stale",
		ChainInput:    &followerOut,
		StemInput:     lrb.Stem,
		Timestamp:     branchTs,
		SignatureType: base.SignatureTypeED25519,
		PrivateKey:    td.privKeyAux,
		PublicKey:     auxPub,
		StemAggregates: &txbuilder_seq.StemAggregates{
			CoverageDelta:            *coverage(branchTs, true),
			BaselineFrozenCoverage:   lrb.FrozenCoverage,
			NumConfirmedTransactions: 2,
			NumSeqTransactions:       2,
			NumSeq:                   1,
			BaselineRoot:             lrb.Root.Bytes(),
		},
	})
	require.NoError(t, err)
	branchTx, _ := parse(branchBytes)
	td.wrk.TxBytesInFromPeerQueued(branchBytes, nil, peer.ID("some peer"), branchTx.ID())

	deadline = time.Now().Add(15 * time.Second)
	for {
		br := td.wrk.GetVertex(branchTx.ID())
		if br != nil && br.GetTxStatus() != vertex.Undefined {
			require.Equal(t, vertex.Good, br.GetTxStatus(), "the branch must attach Good: %v", br.GetError())
			break
		}
		require.True(t, time.Now().Before(deadline), "the branch did not attach")
		time.Sleep(ledger.TickDuration())
	}
	for _, txid := range []base.TransactionID{bootstrapTx.ID(), followerTx.ID()} {
		vid := td.wrk.GetVertex(txid)
		require.NotNil(t, vid)
		require.Equal(t, vertex.Good, vid.GetTxStatus(), "%s: %v", txid.StringShort(), vid.GetError())
	}

	td.waitStop()
}
