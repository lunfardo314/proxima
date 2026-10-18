package mine

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// Branch selection in the miner's tree of transits. The miner descends from the
// confirmed root through the canonical child at every fork, the choice the
// sequencers make too. What these tests pin is that choice, because the
// obvious ones (tallest branch, first seen) reintroduce the very bias the
// stream removes.

// treeTip builds a tip stand-in at the given height and (optional) successor
// slot. `who` distinguishes competing transits at the same height; the height is
// folded into the ID too, so tips on one branch stay distinct. The slot goes
// into the ID's timestamp, which is where the tie-break reads it from.
func treeTip(height uint64, who byte, slot ...uint32) *mineTip {
	s := uint32(0)
	if len(slot) > 0 {
		s = slot[0]
	}
	var txid base.TransactionID
	copy(txid[:base.LedgerTimeByteLength], base.T(s, 1).Bytes())
	txid[len(txid)-1] = who
	txid[len(txid)-2] = byte(height)
	return &mineTip{
		oid:     base.MustNewOutputID(txid, 0),
		cc:      &txbuildercore.ChainConstraintView{TransitionCounter: height},
		ml:      &txbuildercore.MineLockView{R: 1_000_000},
		balance: 1,
	}
}

// ownPayout is the reward layout stand-in that marks a transit as this miner's.
func ownPayout(own bool) *minePayout {
	if own {
		return &minePayout{option: txbuildercore.MinePayoutFull}
	}
	return nil
}

// insertTip adds a transit extending `parent`.
func insertTip(t *testing.T, tr *mineTree, parent *mineTip, height uint64, who byte, own bool) *mineTip {
	t.Helper()
	tip := treeTip(height, who)
	require.True(t, tr.insert(tip.oid.TransactionID(), parent.oid, tip, ownPayout(own)))
	return tip
}

// insertTipSlot is insertTip with an explicit successor slot on the transit, so
// a test can exercise the oldest-slot tie-break.
func insertTipSlot(t *testing.T, tr *mineTree, parent *mineTip, height uint64, who byte, slot uint32, own bool) *mineTip {
	t.Helper()
	tip := treeTip(height, who, slot)
	require.True(t, tr.insert(tip.oid.TransactionID(), parent.oid, tip, ownPayout(own)))
	return tip
}

// insertTipVRF is insertTip with an explicit VRF output on the transit (all at
// the same slot, so the VRF output is the deciding tie-break).
func insertTipVRF(t *testing.T, tr *mineTree, parent *mineTip, height uint64, who byte, vrfOutput []byte, own bool) *mineTip {
	t.Helper()
	tip := treeTip(height, who)
	tip.vrfOutput = vrfOutput
	require.True(t, tr.insert(tip.oid.TransactionID(), parent.oid, tip, ownPayout(own)))
	return tip
}

// the longest branch is the one extended
func TestMineTreeFollowsLongest(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)
	require.Equal(t, root.oid, tr.bestTip().oid, "with nothing tracked the root is the tip")

	a := insertTip(t, tr, root, 6, 1, false)
	require.Equal(t, a.oid, tr.bestTip().oid)

	b := insertTip(t, tr, a, 7, 1, false)
	require.Equal(t, b.oid, tr.bestTip().oid, "the higher transit wins")
}

// The case seen on the take 1 network at low difficulty: a miner whose candidate
// lost the slot extends it at once, which makes its branch taller than the
// canonical winner. The sequencers keep only successors of the winner, so the
// miner must leave its own taller branch and extend the winner; following the
// tallest branch orphaned whole slots and drove the difficulty down.
func TestMineTreeCanonicalBeatsTaller(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	// two candidates for the same slot: ours has the bigger VRF output and loses
	lost := insertTipVRF(t, tr, root, 6, 1, []byte{0x80}, true)
	winner := insertTipVRF(t, tr, root, 6, 2, []byte{0x10}, false)
	// our quick successor on the losing candidate is one transit taller
	insertTip(t, tr, lost, 7, 1, true)
	require.Equal(t, winner.oid, tr.bestTip().oid, "the canonical winner must beat a taller branch on a losing candidate")

	// once the winner is extended, its successor is the tip
	next := insertTip(t, tr, winner, 7, 3, false)
	require.Equal(t, next.oid, tr.bestTip().oid)

	// a deeper branch off the loser changes nothing
	deep := insertTip(t, tr, lost, 7, 4, false)
	deeper := insertTip(t, tr, deep, 8, 4, false)
	insertTip(t, tr, deeper, 9, 4, false)
	require.Equal(t, next.oid, tr.bestTip().oid, "depth below a losing candidate must not count")
}

// the bound on tracked transits never drops the best branch: losing a node near
// the root would lose everything the descent reaches through it
func TestMineTreeBoundSparesBestBranch(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)
	a := insertTipVRF(t, tr, root, 6, 1, []byte{0x00}, false)
	b := insertTipVRF(t, tr, a, 7, 1, []byte{0x00}, false)

	// a flood of losing candidates at the lowest height above the root
	for i := 0; i < mineTreeMaxNodes*2; i++ {
		tip := treeTip(6, byte(i%251+2))
		tip.oid = base.MustNewOutputID(randTxID(byte(i), byte(i>>8)+1), 0)
		tip.vrfOutput = []byte{0xff}
		tr.insert(tip.oid.TransactionID(), root.oid, tip, nil)
	}
	_, _, tracked, _ := tr.stats()
	require.LessOrEqual(t, tracked, mineTreeMaxNodes)
	require.Equal(t, b.oid, tr.bestTip().oid, "the best branch must survive the bound")
}

// A tie must NOT go to the transit seen first. A miner sees its own transit
// immediately and everyone else's a gossip hop later, so first-seen would mean
// always preferring one's own — which is exactly the winner-take-all ratchet. It
// goes to the oldest slot instead, which under the pace-relieved difficulty is
// the heaviest transit.
func TestMineTreeTieIgnoresArrivalOrderAndOwnership(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	// ours arrives first but at a LATER slot (a bigger gap, so a lower required K)
	insertTipSlot(t, tr, root, 6, 1, 200, true)
	// a competitor's arrives later at an OLDER slot (heavier)
	better := insertTipSlot(t, tr, root, 6, 2, 100, false)

	require.Equal(t, better.oid, tr.bestTip().oid,
		"a tie must go to the oldest slot, not the first seen nor to our own")
}

// with equal height, slot and VRF output the tie is broken deterministically,
// so every honest miner converges on the same branch
func TestMineTreeTieBreaksOnTxID(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	insertTip(t, tr, root, 6, 9, false)
	insertTip(t, tr, root, 6, 2, false)

	best := tr.bestTip()
	require.Equal(t, byte(2), best.oid.TransactionID()[len(base.TransactionID{})-1],
		"equal height, slot and VRF output must break on the lower txid")
}

// with equal height and slot, the smaller VRF output wins: it is the canonical
// winner the sequencers pick too. The lower-txid transit (which the plain txid
// rule would pick) must lose to the smaller VRF output.
func TestMineTreeTieBreaksOnVRFOutput(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	// who=2 has the lower txid but the bigger VRF output; who=9 has the smaller one
	insertTipVRF(t, tr, root, 6, 2, []byte{0x80, 0x00}, false)
	insertTipVRF(t, tr, root, 6, 9, []byte{0x7f, 0xff}, false)

	best := tr.bestTip()
	require.Equal(t, []byte{0x7f, 0xff}, best.vrfOutput, "equal height and slot must break on the smaller VRF output")
	require.Equal(t, byte(9), best.oid.TransactionID()[len(base.TransactionID{})-1])
}

// the VRF output is only a tie-break among equal slots: an older slot (heavier)
// must still win against a smaller VRF output, since the slot is what cannot be
// ground.
func TestMineTreeSlotDominatesVRFOutput(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	// a later slot but the smallest possible VRF output
	weak := treeTip(6, 1, 200)
	weak.vrfOutput = []byte{0x00, 0x00}
	require.True(t, tr.insert(weak.oid.TransactionID(), root.oid, weak, nil))
	// an older slot, a large VRF output
	strong := treeTip(6, 2, 100)
	strong.vrfOutput = []byte{0xff, 0xff}
	require.True(t, tr.insert(strong.oid.TransactionID(), root.oid, strong, nil))

	require.Equal(t, strong.oid, tr.bestTip().oid, "an older slot must beat a smaller VRF output")
}

// the older slot wins even against a transit inserted later
func TestMineTreeOlderSlotWins(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	strong := insertTipSlot(t, tr, root, 6, 1, 100, false)
	insertTipSlot(t, tr, root, 6, 2, 200, false)

	require.Equal(t, strong.oid, tr.bestTip().oid)
}

// the loop is told when the branch it is extending stops being the best one
func TestMineTreeSupersededSignalsTheLoop(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	mining := insertTip(t, tr, root, 6, 1, true)
	require.Equal(t, mining.oid, tr.takeBestForMining().oid)
	require.False(t, tr.superseded(), "still the best branch")

	insertTip(t, tr, mining, 7, 2, false)
	require.True(t, tr.superseded(), "a longer branch must supersede the target")
}

// a transit whose predecessor has not arrived is parked and released later:
// stream frames can overtake one another
func TestMineTreePendingReleasedWhenParentArrives(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	missingParent := treeTip(6, 1)
	tr.tipOrQueue(missingParent.oid, []byte("child-tx"))
	require.Empty(t, tr.takePending(root.oid), "nothing waits on the root")

	waiting := tr.takePending(missingParent.oid)
	require.Len(t, waiting, 1)
	require.Equal(t, []byte("child-tx"), waiting[0].txBytes)
	require.Empty(t, tr.takePending(missingParent.oid), "taking is destructive")
}

// parked transits do not accumulate for ever
func TestMineTreePendingExpires(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	stale := treeTip(6, 1)
	tr.tipOrQueue(stale.oid, []byte("old"))
	tr.mu.Lock()
	for _, lst := range tr.pending {
		for _, p := range lst {
			p.received = time.Now().Add(-2 * mineOrphanTTL)
		}
	}
	tr.mu.Unlock()

	// queuing another transit sweeps expired entries
	tr.tipOrQueue(treeTip(7, 2).oid, []byte("fresh"))
	require.Empty(t, tr.takePending(stale.oid), "expired entries must be swept")
}

// A predecessor that arrived before the stream callback acquired the lock
// must be returned directly, including when confirmation made it the root.
func TestMineTreePendingKnownParent(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)
	parent := insertTip(t, tr, root, 6, 1, false)

	require.Same(t, root, tr.tipOrQueue(root.oid, []byte("root-child")))
	require.Same(t, parent, tr.tipOrQueue(parent.oid, []byte("child")))
	tr.onConfirmed(parent)
	require.Same(t, parent, tr.tipOrQueue(parent.oid, []byte("child")))
	require.Empty(t, tr.pending, "known predecessors must not leave parked transits")
}

// A quiet stream must not replay expired candidates just because no newer
// arrival triggered expiry while their predecessor was missing.
func TestMineTreePendingExpiresBeforeReplay(t *testing.T) {
	tr := newMineTree(treeTip(5, 0))
	parent := treeTip(6, 1)
	tr.tipOrQueue(parent.oid, []byte("old"))
	tr.pending[parent.oid][0].received = time.Now().Add(-2 * mineOrphanTTL)

	require.Empty(t, tr.takePending(parent.oid))
}

// Keeping unknown descendants across confirmation must remain bounded even
// when all queued candidates spend the same missing predecessor.
func TestMineTreePendingBound(t *testing.T) {
	for _, sameParent := range []bool{true, false} {
		name := "distinct parents"
		if sameParent {
			name = "same parent"
		}
		t.Run(name, func(t *testing.T) {
			tr := newMineTree(treeTip(5, 0))
			parent := treeTip(6, 1)
			for i := range mineTreeMaxNodes {
				if !sameParent {
					parent = treeTip(uint64(i+6), byte(i>>8)+1)
				}
				require.Nil(t, tr.tipOrQueue(parent.oid, []byte("candidate")))
			}
			tr.tipOrQueue(parent.oid, []byte("overflow in existing bucket"))
			extra := treeTip(6, 3)
			tr.tipOrQueue(extra.oid, []byte("overflow in new bucket"))
			count := 0
			for _, waiting := range tr.pending {
				count += len(waiting)
			}
			require.Equal(t, mineTreeMaxNodes, count)
			require.Empty(t, tr.takePending(extra.oid))

			require.NotEmpty(t, tr.takePending(parent.oid))
			tr.tipOrQueue(extra.oid, []byte("room after replay"))
			require.Len(t, tr.takePending(extra.oid), 1)
		})
	}
}

// confirming our own transit counts it and keeps the branch
func TestMineTreeConfirmOwn(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)
	own := insertTip(t, tr, root, 6, 1, true)

	verdict, ownConfirmed := tr.onConfirmed(own)
	require.Equal(t, tipConfirmedOurs, verdict)
	require.Equal(t, 1, ownConfirmed)

	confirmed, inFlight, _, _ := tr.stats()
	require.EqualValues(t, 6, confirmed)
	require.Zero(t, inFlight)
}

// the LRB can settle several heights at once; because the chain is a singleton
// a confirmed successor implies its whole predecessor chain
func TestMineTreeConfirmCountsWholeOwnLineage(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)
	a := insertTip(t, tr, root, 6, 1, true)
	b := insertTip(t, tr, a, 7, 1, true)
	c := insertTip(t, tr, b, 8, 1, true)

	verdict, ownConfirmed := tr.onConfirmed(c)
	require.Equal(t, tipConfirmedOurs, verdict)
	require.Equal(t, 3, ownConfirmed, "all three settled heights were ours")
}

// a competitor's transit confirming drops our branch above it
func TestMineTreeConfirmCompetitorReanchors(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)
	ours := insertTip(t, tr, root, 6, 1, true)
	insertTip(t, tr, ours, 7, 1, true)

	competitor := treeTip(6, 2)
	verdict, ownConfirmed := tr.onConfirmed(competitor)
	require.Equal(t, tipReanchor, verdict)
	require.Zero(t, ownConfirmed)

	confirmed, _, tracked, orphaned := tr.stats()
	require.EqualValues(t, 6, confirmed)
	require.Zero(t, tracked, "our whole branch above the confirmed height is dropped")
	require.Equal(t, 2, orphaned)
	require.Equal(t, competitor.oid, tr.bestTip().oid, "the confirmed tip becomes the tip to extend")
}

// an LRB that has not caught up is not a signal
func TestMineTreeConfirmLaggingIsNoChange(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)
	insertTip(t, tr, root, 6, 1, true)

	verdict, _ := tr.onConfirmed(treeTip(4, 0))
	require.Equal(t, tipNoChange, verdict)

	_, _, tracked, _ := tr.stats()
	require.Equal(t, 1, tracked, "nothing is dropped on a lagging LRB")
}

// re-rooting drops branches that do not descend from the new root
func TestMineTreePrunesForeignBranches(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)
	ourA := insertTip(t, tr, root, 6, 1, true)
	insertTip(t, tr, ourA, 7, 1, true)
	rival := insertTip(t, tr, root, 6, 2, false)
	rivalChild := insertTip(t, tr, rival, 7, 2, false)

	// the rival branch is confirmed at height 6
	tr.onConfirmed(rival)

	_, _, tracked, _ := tr.stats()
	require.Equal(t, 1, tracked, "only the rival's descendant survives")
	require.Equal(t, rivalChild.oid, tr.bestTip().oid)
}

// transits at or below the confirmed height are never tracked
func TestMineTreeRejectsSettledHeights(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	stale := treeTip(5, 3)
	require.False(t, tr.insert(stale.oid.TransactionID(), root.oid, stale, nil))
	require.False(t, tr.insert(treeTip(4, 3).oid.TransactionID(), root.oid, treeTip(4, 3), nil))
}

// the same transit arriving twice (two subscribed nodes) is tracked once
func TestMineTreeIgnoresDuplicates(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	tip := treeTip(6, 1)
	require.True(t, tr.insert(tip.oid.TransactionID(), root.oid, tip, nil))
	require.False(t, tr.insert(tip.oid.TransactionID(), root.oid, tip, nil))

	_, _, tracked, _ := tr.stats()
	require.Equal(t, 1, tracked)
}

// the tree stays bounded under a flood of valid transits
func TestMineTreeStaysBounded(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	for i := 0; i < mineTreeMaxNodes*2; i++ {
		tip := treeTip(uint64(6+i/4), byte(i%251+1))
		tip.oid = base.MustNewOutputID(randTxID(byte(i), byte(i>>8)), 0)
		tr.insert(tip.oid.TransactionID(), root.oid, tip, nil)
	}
	_, _, tracked, _ := tr.stats()
	require.LessOrEqual(t, tracked, mineTreeMaxNodes)
}

func randTxID(a, b byte) base.TransactionID {
	var txid base.TransactionID
	txid[len(txid)-1] = a
	txid[len(txid)-2] = b
	return txid
}

// A branch that never confirms while nobody else takes the height means our
// submissions are not reaching the ledger at all — distinct from losing races,
// which shows up as a competitor's tip confirming.
func TestMineTreeStallDetection(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)
	require.False(t, tr.stalledFor(time.Hour), "nothing in flight is never a stall")

	insertTip(t, tr, root, 6, 1, true)
	require.False(t, tr.stalledFor(time.Hour), "in flight but not yet overdue")

	tr.mu.Lock()
	tr.lastConfirmedAt = time.Now().Add(-2 * time.Hour)
	tr.mu.Unlock()
	require.True(t, tr.stalledFor(time.Hour))

	// a confirmation clears it
	tr.onConfirmed(treeTip(6, 2))
	require.False(t, tr.stalledFor(time.Hour))
}

// The placement reads what the unconfirmed own transits did. Only own transits
// above the confirmed root on the branch being extended count, oldest first; a
// new delegation gets the chain ID of its origin output, the transit's output 1;
// and a confirmation that moves the root past a transit retires its entry.
func TestSpeculativePayouts(t *testing.T) {
	root := treeTip(0, 0)
	tr := newMineTree(root)
	target := base.ChainID{7}
	opening := &minePayout{option: txbuildercore.MinePayoutOpening}
	delegation := &minePayout{option: txbuildercore.MinePayoutDelegation, target: target, cut: 950}
	h1 := treeTip(1, 1)
	require.True(t, tr.insert(h1.oid.TransactionID(), root.oid, h1, opening))
	h2 := treeTip(2, 1)
	require.True(t, tr.insert(h2.oid.TransactionID(), h1.oid, h2, delegation))
	h3 := insertTip(t, tr, h2, 3, 2, false) // someone else's on top, extended next

	spec := tr.speculativePayouts()
	require.Len(t, spec, 2)
	require.EqualValues(t, txbuildercore.MinePayoutOpening, spec[0].option)
	require.EqualValues(t, txbuildercore.MinePayoutDelegation, spec[1].option)
	require.Equal(t, target, spec[1].target)
	require.Equal(t, base.MakeOriginChainID(base.MustNewOutputID(h2.oid.TransactionID(), 1)), spec[1].delegationID)

	// the ledger settles through h2: both own transits are in the state now
	verdict, own := tr.onConfirmed(h2)
	require.Equal(t, tipConfirmedOurs, verdict)
	require.Equal(t, 2, own)
	require.Empty(t, tr.speculativePayouts())
	require.Equal(t, h3.oid, tr.bestTip().oid)
}

// Merging the speculative branch into the delegations read from the state: a
// pending delegation appears once with the amount a new one holds, a pending
// top-up is credited to the delegation it names, and one the state already
// shows is not added twice.
func TestWithSpeculative(t *testing.T) {
	inState := base.ChainID{1}
	pending := base.ChainID{2}
	dels := []*ownDelegation{{view: &txbuildercore.DelegationOutputView{ChainID: inState}, balance: 1000}}
	spec := []minePayout{
		{option: txbuildercore.MinePayoutDelegation, delegationID: pending, target: base.ChainID{7}, cut: 950},
		{option: txbuildercore.MinePayoutFull, topUp: true, delegationID: inState, minted: 95},
		{option: txbuildercore.MinePayoutFull, topUp: true, delegationID: pending, minted: 95},
		{option: txbuildercore.MinePayoutDelegation, delegationID: inState},
		{option: txbuildercore.MinePayoutFull, topUp: true, delegationID: base.ChainID{9}, minted: 95}, // unknown: ignored
	}
	out := withSpeculative(dels, spec, 80)
	require.Len(t, out, 2)
	require.EqualValues(t, 1095, out[0].balance)
	require.Equal(t, pending, out[1].view.ChainID)
	require.EqualValues(t, 175, out[1].balance)
	require.False(t, out[1].frozen)
}

// A miner whose pinned delegation target is unavailable draws by rating
// instead of falling to the opening; a pinned target that is active is taken
// as pinned; no pinned target is the plain draw.
func TestPickDelegationTarget(t *testing.T) {
	a := txbuildercore.SequencerCandidate{ID: base.ChainID{1}, ShareLeft: 900}
	b := txbuildercore.SequencerCandidate{ID: base.ChainID{2}, ShareLeft: 950}
	active := []txbuildercore.SequencerCandidate{a, b}
	first := func(int) int { return 0 }

	got, fellBack, err := pickDelegationTarget(active, &a.ID, first)
	require.NoError(t, err)
	require.False(t, fellBack)
	require.Equal(t, a.ID, got.ID)

	dead := base.ChainID{9}
	got, fellBack, err = pickDelegationTarget(active, &dead, first)
	require.NoError(t, err)
	require.True(t, fellBack)
	require.Contains(t, []base.ChainID{a.ID, b.ID}, got.ID)

	_, fellBack, err = pickDelegationTarget(nil, &dead, first)
	require.Error(t, err)
	require.False(t, fellBack)

	_, fellBack, err = pickDelegationTarget(active, nil, first)
	require.NoError(t, err)
	require.False(t, fellBack)
}
