package mine

import (
	"bytes"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// The miner's view of the mine chain: a tree of verified transits rooted at the
// last LRB-confirmed one, from which the miner picks the branch to extend.
//
// The miner extends the branch the sequencers will settle: from the confirmed
// root down, at every fork the canonical child, the same choice the sequencers
// make among transits on one predecessor. Not the longest branch: at a low
// difficulty a miner extends its own losing candidate at once, the taller branch
// draws everyone off the canonical one, the sequencers orphan it all and the
// slot stays empty. Among the children of one transit the obvious rules are both
// wrong too:
//
//   - first-seen would mean always preferring one's own transit, since a miner
//     sees its own at once and everyone else's a gossip hop later. That is
//     exactly the winner-take-all ratchet this whole mechanism exists to remove.
//   - lowest-txid is grindable: at low difficulty a miner finds several
//     solutions per pace and would simply submit the most favourable one.
//
// So the child at the OLDEST (smallest) successor slot wins. Under the
// pace-relieved difficulty K = max(B - (M - P), E) an earlier slot is a shorter
// gap and so a higher required K, which makes the oldest-slot transit the
// heaviest one — roughly equivalent to "switch to the heaviest difficulty". It
// is non-grindable the way a raw trailing-zero count is not: to claim an older
// slot a miner must actually meet the higher K the constraint requires there.
// Among equal slots the SMALLEST VRF output wins. It is fixed by the miner's key
// and the message, so the only way to improve it is another valid solution,
// which is more work; and the smallest of independent draws is equally likely
// to be anyone's, so the rule does not change who wins how often. The lower
// txid is the final deterministic fallback. Every honest miner then converges
// on the same branch, and so do the sequencers, which apply the same rule when
// they choose which of the competing transits to include
// (sequencer/task/mine_transits.go). See kb/mine_conflict_rule.md.
//
// This is a client convention, not consensus: the ledger still decides through
// the sequencers. A miner that ignores it and clings to its own branch is just
// more likely to be orphaned.

const (
	// hard cap on tracked transits; beyond it the lowest heights are dropped
	mineTreeMaxNodes = 512
	// heights this far below the confirmed root are pruned
	mineTreeKeepBelowRoot = 8
	// how long a transit whose predecessor has not arrived is held
	mineOrphanTTL = 30 * time.Second
)

// mineTreeNode is one verified transit.
type mineTreeNode struct {
	txid      base.TransactionID
	parent    base.OutputID // the mine output this transit spends
	tip       *mineTip      // the mine output it produces
	height    uint64        // == tip.cc.TransitionCounter
	txSlot    uint32        // == tip.oid.Timestamp().Slot; the OLDEST slot wins a tie (heaviest K)
	vrfOutput []byte        // == tip.vrfOutput; the SMALLEST wins among equal slots
	own       bool          // this miner produced it
	// included: a sequencer milestone has consumed it (seen on the stream). It
	// is then the child that counts, whatever the rule would have predicted.
	included bool
	payout   *minePayout // the reward layout of an own transit, nil for a foreign one
}

// pendingTransit is a verified-shape transit whose predecessor is not known yet.
type pendingTransit struct {
	txBytes  []byte
	parent   base.OutputID
	received time.Time
}

type mineTree struct {
	mu sync.Mutex

	root  *mineTip                        // last LRB-confirmed tip
	nodes map[base.OutputID]*mineTreeNode // keyed by the tip each transit produces
	best  *mineTreeNode                   // head of the branch to extend; nil = root

	// transits waiting for their predecessor, keyed by the input they spend
	pending map[base.OutputID][]*pendingTransit
	// inclusion notices for transits not in the tree yet, by when they arrived
	includedPending map[base.TransactionID]time.Time

	// the tip the mining loop is currently extending, so the tree can tell it
	// when its target stops being the best branch
	miningOn base.OutputID

	confirmed       uint64 // height of the root
	orphaned        int
	lastConfirmedAt time.Time
}

func newMineTree(root *mineTip) *mineTree {
	t := &mineTree{
		nodes:           make(map[base.OutputID]*mineTreeNode),
		pending:         make(map[base.OutputID][]*pendingTransit),
		includedPending: make(map[base.TransactionID]time.Time),
	}
	t.setRoot(root)
	return t
}

// setRoot re-anchors on a confirmed tip, dropping every branch not descended
// from it.
func (t *mineTree) setRoot(root *mineTip) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.setRootLocked(root)
}

func (t *mineTree) setRootLocked(root *mineTip) {
	t.root = root
	t.confirmed = root.cc.TransitionCounter
	t.lastConfirmedAt = time.Now()

	// keep only what still descends from the new root
	for oid, n := range t.nodes {
		if !t.descendsFromLocked(n) || n.height <= t.confirmed {
			delete(t.nodes, oid)
		}
	}
	// Parked transits are left to their TTL: the stream runs ahead of the LRB,
	// so a predecessor is often known only once it confirms, and the transits
	// parked on it, or on its own unknown successors, are what comes next.
	t.expirePendingLocked()
	t.recomputeBestLocked()
}

// descendsFromLocked walks parent links back to the root.
func (t *mineTree) descendsFromLocked(n *mineTreeNode) bool {
	for i := 0; i <= mineTreeMaxNodes; i++ {
		if n.parent == t.root.oid {
			return true
		}
		p, ok := t.nodes[n.parent]
		if !ok {
			return false
		}
		n = p
	}
	return false
}

// insert adds a transit that has already been verified against its predecessor;
// payout is set for this miner's own transits and nil for anyone else's.
// Returns true if it was added.
func (t *mineTree) insert(txid base.TransactionID, parent base.OutputID, tip *mineTip, payout *minePayout) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.insertLocked(txid, parent, tip, payout)
}

func (t *mineTree) insertLocked(txid base.TransactionID, parent base.OutputID, tip *mineTip, payout *minePayout) bool {
	if _, exists := t.nodes[tip.oid]; exists {
		return false
	}
	if tip.cc.TransitionCounter <= t.confirmed {
		return false // settled or already orphaned
	}
	n := &mineTreeNode{
		txid:      txid,
		parent:    parent,
		tip:       tip,
		height:    tip.cc.TransitionCounter,
		txSlot:    tip.oid.Timestamp().Slot,
		vrfOutput: tip.vrfOutput,
		own:       payout != nil,
		payout:    payout,
	}
	t.nodes[tip.oid] = n
	if _, ok := t.includedPending[txid]; ok {
		delete(t.includedPending, txid)
		t.markIncludedLocked(n)
	}
	t.enforceBoundsLocked()
	t.recomputeBestLocked()
	return true
}

// enforceBoundsLocked keeps the tree bounded by dropping the lowest heights off
// the best branch. The best branch itself is spared: it is found by descending
// from the root, so losing a node near the root would lose the whole branch.
func (t *mineTree) enforceBoundsLocked() {
	onPath := make(map[*mineTreeNode]bool)
	for n := t.best; n != nil; n = t.nodes[n.parent] {
		onPath[n] = true
	}
	for len(t.nodes) > mineTreeMaxNodes {
		var lowest *mineTreeNode
		for _, n := range t.nodes {
			if !onPath[n] && (lowest == nil || n.height < lowest.height) {
				lowest = n
			}
		}
		if lowest == nil {
			return
		}
		delete(t.nodes, lowest.tip.oid)
	}
}

// betterThan is the canonical order among transits on one predecessor: a
// transit a sequencer has included first, then the oldest slot, then the
// smallest VRF output, then txid, as in
// sequencer/task/mine_transits.go. The oldest (smallest) successor slot wins
// because the pace-relieved difficulty makes it the heaviest transit (a shorter
// gap requires a higher K), and unlike a raw trailing-zero count it is
// non-grindable — claiming an older slot costs the higher K the constraint
// demands there. The VRF output sits AFTER the slot on purpose: the slot is the
// anti-grinding rail, so it must dominate; the VRF output only breaks ties
// among equal-slot transits, and a miner cannot choose it, only find another
// valid solution.
func (n *mineTreeNode) betterThan(other *mineTreeNode) bool {
	switch {
	case other == nil:
		return true
	case n.included != other.included:
		// The rule predicts what the sequencers will include; once one has
		// included a transit, that is what the next transit must extend. A
		// prediction that disagrees with it only builds on a loser, which is
		// how a sequencer taking transits early could hand the next slots to
		// the one miner who knew its transit had been taken.
		return n.included
	case n.txSlot != other.txSlot:
		return n.txSlot < other.txSlot
	case !bytes.Equal(n.vrfOutput, other.vrfOutput):
		return bytes.Compare(n.vrfOutput, other.vrfOutput) < 0
	default:
		return bytes.Compare(n.txid[:], other.txid[:]) < 0
	}
}

// recomputeBestLocked descends from the root through the canonical child of
// every transit; the last one reached is the tip to extend.
func (t *mineTree) recomputeBestLocked() {
	var best *mineTreeNode
	parent := t.root.oid
	for i := 0; i <= mineTreeMaxNodes; i++ {
		var next *mineTreeNode
		for _, n := range t.nodes {
			if n.parent == parent && n.betterThan(next) {
				next = n
			}
		}
		if next == nil {
			break
		}
		best, parent = next, next.tip.oid
	}
	t.best = best
}

// bestTip is the mine output the miner should extend, and whether it is a
// speculative transit rather than the confirmed root.
func (t *mineTree) bestTip() *mineTip {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.best == nil {
		return t.root
	}
	return t.best.tip
}

// takeBestForMining picks the tip to extend and records it as the mining
// target in one step. Selecting and recording under a single lock matters: a
// transit arriving in between would otherwise be judged against the previous
// target, either aborting the round that was just started or — worse — leaving
// a superseded round running to its deadline.
func (t *mineTree) takeBestForMining() *mineTip {
	t.mu.Lock()
	defer t.mu.Unlock()

	tip := t.root
	if t.best != nil {
		tip = t.best.tip
	}
	t.miningOn = tip.oid
	return tip
}

// bestOnParent is the canonical winner so far among the tracked transits that
// spend parent, as its VRF output and slot; ok is false when none is tracked.
func (t *mineTree) bestOnParent(parent base.OutputID) (vrfOutput []byte, slot uint32, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var best *mineTreeNode
	for _, n := range t.nodes {
		if n.parent == parent && n.betterThan(best) {
			best = n
		}
	}
	if best == nil {
		return nil, 0, false
	}
	return best.vrfOutput, best.txSlot, true
}

// superseded reports whether the tip the loop is mining on has left the branch
// to extend. Transits on the tip itself, own or not, only extend that branch:
// the round goes on competing for the same slot.
func (t *mineTree) superseded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.miningOn == t.root.oid {
		return false
	}
	for n := t.best; n != nil; n = t.nodes[n.parent] {
		if n.tip.oid == t.miningOn {
			return false
		}
	}
	return true
}

// rootTip is the confirmed tip the tree is anchored on.
func (t *mineTree) rootTip() *mineTip {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.root
}

// tipOrQueue returns a known predecessor or parks the transit until it arrives.
// Lookup and queuing must be atomic with insertion and confirmation, otherwise
// a parent can arrive and drain its queue just before the child is parked.
func (t *mineTree) tipOrQueue(parent base.OutputID, txBytes []byte) *mineTip {
	t.mu.Lock()
	defer t.mu.Unlock()

	if pred := t.tipForLocked(parent); pred != nil {
		return pred
	}
	// Bound transits, not parent buckets: many candidates may spend one parent.
	if t.expirePendingLocked() >= mineTreeMaxNodes {
		return nil
	}
	t.pending[parent] = append(t.pending[parent], &pendingTransit{
		txBytes:  txBytes,
		parent:   parent,
		received: time.Now(),
	})
	return nil
}

func (t *mineTree) expirePendingLocked() (count int) {
	deadline := time.Now().Add(-mineOrphanTTL)
	for oid, lst := range t.pending {
		kept := lst[:0]
		for _, p := range lst {
			if p.received.After(deadline) {
				kept = append(kept, p)
			}
		}
		clear(lst[len(kept):])
		if len(kept) == 0 {
			delete(t.pending, oid)
		} else {
			t.pending[oid] = kept
			count += len(kept)
		}
	}
	for txid, received := range t.includedPending {
		if !received.After(deadline) {
			delete(t.includedPending, txid)
		}
	}
	return
}

// markIncluded records that a sequencer milestone has consumed the transit, and
// reports whether that moved the branch to extend. A notice for a transit not
// in the tree yet is kept for mineOrphanTTL and applied when it arrives.
func (t *mineTree) markIncluded(txid base.TransactionID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, n := range t.nodes {
		if n.txid == txid {
			before := t.best
			t.markIncludedLocked(n)
			t.recomputeBestLocked()
			return t.best != before
		}
	}
	if len(t.includedPending) < mineTreeMaxNodes {
		t.includedPending[txid] = time.Now()
	}
	return false
}

// markIncludedLocked marks the transit and its ancestors: including a transit
// includes its whole past.
func (t *mineTree) markIncludedLocked(n *mineTreeNode) {
	for ; n != nil && !n.included; n = t.nodes[n.parent] {
		n.included = true
	}
}

// takePending removes and returns the transits waiting on a given predecessor,
// so they can be verified now that it exists.
func (t *mineTree) takePending(parent base.OutputID) []*pendingTransit {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.expirePendingLocked()
	lst := t.pending[parent]
	delete(t.pending, parent)
	return lst
}

// tipFor returns the mine output with the given ID if the tree knows it, so a
// streamed transit can be verified against its predecessor.
func (t *mineTree) tipFor(oid base.OutputID) *mineTip {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.tipForLocked(oid)
}

func (t *mineTree) tipForLocked(oid base.OutputID) *mineTip {
	if oid == t.root.oid {
		return t.root
	}
	if n, ok := t.nodes[oid]; ok {
		return n.tip
	}
	return nil
}

// onConfirmed folds an LRB-confirmed tip into the tree and reports what it
// means for this miner. Because the chain is a singleton, the confirmed height
// tells us which transits settled and the output ID tells us whose they were.
func (t *mineTree) onConfirmed(tip *mineTip) (verdict mineTipVerdict, ownConfirmed int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	c := tip.cc.TransitionCounter
	if c <= t.confirmed {
		return tipNoChange, 0
	}

	// count how many of the newly settled heights were ours by walking back
	// from the confirmed tip through the branch we tracked
	own := 0
	settled := tip
	for h := c; h > t.confirmed; h-- {
		n, ok := t.nodes[settled.oid]
		if !ok {
			break
		}
		if n.own {
			own++
		}
		parent, ok := t.nodes[n.parent]
		if !ok {
			break
		}
		settled = parent.tip
	}

	// everything we tracked above the old root that is not this lineage is dead
	before := len(t.nodes)
	t.setRootLocked(tip)
	if dropped := before - len(t.nodes); dropped > 0 {
		t.orphaned += dropped
	}

	if own == 0 {
		return tipReanchor, 0
	}
	return tipConfirmedOurs, own
}

// speculativePayouts returns the reward layouts of this miner's transits on the
// branch being extended that nothing has confirmed yet, oldest first. The
// delegations they open and the top-ups they carry are not in the reliable
// branch, so a placement that reads only the state would repeat them; a
// transit on the branch that loses is dropped from the tree with it. A new
// delegation's chain ID is derived here from its origin output, the transit's
// output 1.
func (t *mineTree) speculativePayouts() []minePayout {
	t.mu.Lock()
	defer t.mu.Unlock()

	var ret []minePayout
	for n := t.best; n != nil && n.height > t.confirmed; n = t.nodes[n.parent] {
		if n.payout == nil {
			continue
		}
		p := *n.payout
		if p.option == txbuildercore.MinePayoutDelegation {
			p.delegationID = base.MakeOriginChainID(base.MustNewOutputID(n.txid, 1))
		}
		ret = append(ret, p)
	}
	for i, j := 0, len(ret)-1; i < j; i, j = i+1, j-1 {
		ret[i], ret[j] = ret[j], ret[i]
	}
	return ret
}

// stalledFor reports whether transits have been in flight for longer than d
// with nothing confirming. That means our submissions are not reaching the
// ledger at all — a dead tag-along sequencer, a wedged node — rather than that
// we are losing races, which would show up as a competitor's tip confirming.
func (t *mineTree) stalledFor(d time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	inFlight := t.best != nil && t.best.height > t.confirmed
	return inFlight && time.Since(t.lastConfirmedAt) > d
}

// stats returns a snapshot for the totals line.
func (t *mineTree) stats() (confirmed uint64, inFlight, tracked, orphaned int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	best := t.confirmed
	if t.best != nil {
		best = t.best.height
	}
	return t.confirmed, int(best - t.confirmed), len(t.nodes), t.orphaned
}
