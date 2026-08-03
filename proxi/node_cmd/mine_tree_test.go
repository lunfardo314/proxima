package node_cmd

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// Branch selection in the miner's tree of transits. The mine chain is a
// singleton whose transition counter rises by exactly one per transit, so a
// transit is identified by (height, output ID) and "longest chain" means
// highest counter. What these tests pin is the tie-break, because the obvious
// choices reintroduce the very bias the stream removes.

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

// insertTip adds a transit extending `parent`.
func insertTip(t *testing.T, tr *mineTree, parent *mineTip, height uint64, who byte, own bool) *mineTip {
	t.Helper()
	tip := treeTip(height, who)
	require.True(t, tr.insert(tip.oid.TransactionID(), parent.oid, tip, own))
	return tip
}

// insertTipSlot is insertTip with an explicit successor slot on the transit, so
// a test can exercise the oldest-slot tie-break.
func insertTipSlot(t *testing.T, tr *mineTree, parent *mineTip, height uint64, who byte, slot uint32, own bool) *mineTip {
	t.Helper()
	tip := treeTip(height, who, slot)
	require.True(t, tr.insert(tip.oid.TransactionID(), parent.oid, tip, own))
	return tip
}

// insertTipFee is insertTip with an explicit tag-along fee on the transit (all at
// the same slot, so the fee is the deciding tie-break).
func insertTipFee(t *testing.T, tr *mineTree, parent *mineTip, height uint64, who byte, fee uint64, own bool) *mineTip {
	t.Helper()
	tip := treeTip(height, who)
	tip.tagAlongFee = fee
	require.True(t, tr.insert(tip.oid.TransactionID(), parent.oid, tip, own))
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

// with equal height, slot and fee the tie is broken deterministically, so every
// honest miner converges on the same branch
func TestMineTreeTieBreaksOnTxID(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	insertTip(t, tr, root, 6, 9, false)
	insertTip(t, tr, root, 6, 2, false)

	best := tr.bestTip()
	require.Equal(t, byte(2), best.oid.TransactionID()[len(base.TransactionID{})-1],
		"equal height, slot and fee must break on the lower txid")
}

// with equal height and slot, the bigger tag-along fee wins — it is the branch a
// sequencer is more likely to confirm. The lower-txid transit (which the plain
// txid rule would pick) must lose to the higher fee.
func TestMineTreeTieBreaksOnTagAlongFee(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	// who=2 has the lower txid but the smaller fee; who=9 has the bigger fee
	insertTipFee(t, tr, root, 6, 2, 3, false)
	insertTipFee(t, tr, root, 6, 9, 5, false)

	best := tr.bestTip()
	require.EqualValues(t, 5, best.tagAlongFee, "equal height and slot must break on the bigger fee")
	require.Equal(t, byte(9), best.oid.TransactionID()[len(base.TransactionID{})-1])
}

// the fee is only a tie-break among equal slots: an older slot (heavier) must
// still win against a bigger fee, so the fee cannot be used to buy a branch.
func TestMineTreeSlotDominatesFee(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	// a later slot but the maximum fee
	weak := treeTip(6, 1, 200)
	weak.tagAlongFee = 5
	require.True(t, tr.insert(weak.oid.TransactionID(), root.oid, weak, false))
	// an older slot, zero fee
	strong := treeTip(6, 2, 100)
	require.True(t, tr.insert(strong.oid.TransactionID(), root.oid, strong, false))

	require.Equal(t, strong.oid, tr.bestTip().oid, "an older slot must beat a bigger fee")
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
	tr.addPending(missingParent.oid, []byte("child-tx"))
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
	tr.addPending(stale.oid, []byte("old"))
	tr.mu.Lock()
	for _, lst := range tr.pending {
		for _, p := range lst {
			p.received = time.Now().Add(-2 * mineOrphanTTL)
		}
	}
	tr.mu.Unlock()

	// any addPending sweeps expired entries
	tr.addPending(treeTip(7, 2).oid, []byte("fresh"))
	require.Empty(t, tr.takePending(stale.oid), "expired entries must be swept")
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
	require.False(t, tr.insert(stale.oid.TransactionID(), root.oid, stale, false))
	require.False(t, tr.insert(treeTip(4, 3).oid.TransactionID(), root.oid, treeTip(4, 3), false))
}

// the same transit arriving twice (two subscribed nodes) is tracked once
func TestMineTreeIgnoresDuplicates(t *testing.T) {
	root := treeTip(5, 0)
	tr := newMineTree(root)

	tip := treeTip(6, 1)
	require.True(t, tr.insert(tip.oid.TransactionID(), root.oid, tip, false))
	require.False(t, tr.insert(tip.oid.TransactionID(), root.oid, tip, false))

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
		tr.insert(tip.oid.TransactionID(), root.oid, tip, false)
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
