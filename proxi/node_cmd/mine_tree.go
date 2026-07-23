package node_cmd

import (
	"bytes"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
)

// The miner's view of the mine chain: a tree of verified transits rooted at the
// last LRB-confirmed one, from which the miner picks the branch to extend.
//
// The mine chain is a singleton and its chain constraint forces the transition
// counter up by exactly one per transit, so "longest chain" is simply the
// highest counter. Ties need a rule, and the obvious ones are both wrong here:
//
//   - first-seen would mean always preferring one's own transit, since a miner
//     sees its own at once and everyone else's a gossip hop later. That is
//     exactly the winner-take-all ratchet this whole mechanism exists to remove.
//   - lowest-txid is grindable: at low difficulty a miner finds several
//     solutions per pace and would simply submit the most favourable one.
//
// So ties go to the transit carrying the most proof of work — the most trailing
// zero bits — then to the bigger tag-along fee (the branch a sequencer is more
// likely to confirm), with the lower txid as a final deterministic fallback.
// Winning a tie then costs work, doubling per bit, and every honest miner
// converges on the same branch.
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
	txid        base.TransactionID
	parent      base.OutputID // the mine output this transit spends
	tip         *mineTip      // the mine output it produces
	height      uint64        // == tip.cc.TransitionCounter
	powZeros    int
	tagAlongFee uint64 // == tip.tagAlongFee; higher fee is preferred on a tie
	own         bool   // this miner produced it
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

	// the tip the mining loop is currently extending, so the tree can tell it
	// when its target stops being the best branch
	miningOn base.OutputID

	confirmed       uint64 // height of the root
	orphaned        int
	lastConfirmedAt time.Time
}

func newMineTree(root *mineTip) *mineTree {
	t := &mineTree{
		nodes:   make(map[base.OutputID]*mineTreeNode),
		pending: make(map[base.OutputID][]*pendingTransit),
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
	for oid := range t.pending {
		if _, ok := t.nodes[oid]; !ok && oid != t.root.oid {
			delete(t.pending, oid)
		}
	}
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

// insert adds a transit that has already been verified against its predecessor.
// Returns true if it was added.
func (t *mineTree) insert(txid base.TransactionID, parent base.OutputID, tip *mineTip, powZeros int, own bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.insertLocked(txid, parent, tip, powZeros, own)
}

func (t *mineTree) insertLocked(txid base.TransactionID, parent base.OutputID, tip *mineTip, powZeros int, own bool) bool {
	if _, exists := t.nodes[tip.oid]; exists {
		return false
	}
	if tip.cc.TransitionCounter <= t.confirmed {
		return false // settled or already orphaned
	}
	t.nodes[tip.oid] = &mineTreeNode{
		txid:        txid,
		parent:      parent,
		tip:         tip,
		height:      tip.cc.TransitionCounter,
		powZeros:    powZeros,
		tagAlongFee: tip.tagAlongFee,
		own:         own,
	}
	t.enforceBoundsLocked()
	t.recomputeBestLocked()
	return true
}

// enforceBoundsLocked keeps the tree bounded by dropping the lowest heights,
// which are the ones furthest from being extended.
func (t *mineTree) enforceBoundsLocked() {
	for len(t.nodes) > mineTreeMaxNodes {
		var lowest *mineTreeNode
		for _, n := range t.nodes {
			if lowest == nil || n.height < lowest.height {
				lowest = n
			}
		}
		if lowest == nil {
			return
		}
		delete(t.nodes, lowest.tip.oid)
	}
}

// betterThan is the branch preference: height, then work, then tag-along fee,
// then txid. The fee is inserted ahead of the txid fallback because a bigger
// tag-along fee makes the transit more attractive to sequencers, so its branch
// is the more likely to reach the LRB — following it is following the branch
// most likely to confirm. It sits AFTER work on purpose: work is the
// anti-grinding rail (it costs computation that doubles per bit), so it must
// still dominate. The fee only breaks ties among equal-work transits, where it
// steers toward confirmation without letting a miner buy a tie cheaply.
func (n *mineTreeNode) betterThan(other *mineTreeNode) bool {
	switch {
	case other == nil:
		return true
	case n.height != other.height:
		return n.height > other.height
	case n.powZeros != other.powZeros:
		return n.powZeros > other.powZeros
	case n.tagAlongFee != other.tagAlongFee:
		return n.tagAlongFee > other.tagAlongFee
	default:
		return bytes.Compare(n.txid[:], other.txid[:]) < 0
	}
}

func (t *mineTree) recomputeBestLocked() {
	var best *mineTreeNode
	for _, n := range t.nodes {
		if n.betterThan(best) {
			best = n
		}
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

// superseded reports whether the tip the loop is mining on is no longer the
// branch to extend.
func (t *mineTree) superseded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bestOIDLocked() != t.miningOn
}

func (t *mineTree) bestOIDLocked() base.OutputID {
	if t.best == nil {
		return t.root.oid
	}
	return t.best.tip.oid
}

// addPending parks a transit whose predecessor has not arrived yet. Stream
// messages can overtake one another, and a miner that just started has no
// history at all.
func (t *mineTree) addPending(parent base.OutputID, txBytes []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.expirePendingLocked()
	if len(t.pending) >= mineTreeMaxNodes {
		return
	}
	t.pending[parent] = append(t.pending[parent], &pendingTransit{
		txBytes:  txBytes,
		parent:   parent,
		received: time.Now(),
	})
}

func (t *mineTree) expirePendingLocked() {
	deadline := time.Now().Add(-mineOrphanTTL)
	for oid, lst := range t.pending {
		kept := lst[:0]
		for _, p := range lst {
			if p.received.After(deadline) {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			delete(t.pending, oid)
		} else {
			t.pending[oid] = kept
		}
	}
}

// takePending removes and returns the transits waiting on a given predecessor,
// so they can be verified now that it exists.
func (t *mineTree) takePending(parent base.OutputID) []*pendingTransit {
	t.mu.Lock()
	defer t.mu.Unlock()

	lst := t.pending[parent]
	delete(t.pending, parent)
	return lst
}

// tipFor returns the mine output with the given ID if the tree knows it, so a
// streamed transit can be verified against its predecessor.
func (t *mineTree) tipFor(oid base.OutputID) *mineTip {
	t.mu.Lock()
	defer t.mu.Unlock()

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
