package task

import (
	"bytes"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// The canonical winner rule for the mine chain (kb/mine_conflict_rule.md).
//
// Several miners regularly hold a valid transit for the same step, and each one
// reaches the sequencer as an ordinary tag-along. Which of them ends up in the
// ledger used to be decided by whichever a sequencer inserted first, i.e. by
// inclusion order and network position rather than by anything the miners did.
// Instead, all sequencers and all miners rank competing transits the same way:
// the oldest successor slot (the heaviest, under the pace-relieved difficulty),
// then the smallest VRF output, which a miner cannot choose, only improve with
// more work, then the txid as a deterministic fallback. The smallest of
// independent draws is equally likely to be anyone's, so the rule changes who
// wins a contest, not how often anyone wins.
//
// The ledger cannot enforce it, since a transaction does not see its
// competitors; it is a sequencer policy like the health threshold on branches.
// To give competitors time to arrive before the choice is made, a transit is
// held until the settlement window at the end of its slot, right before the
// pre-branch consolidation zone in which no tag-alongs are consumed at all.
// Eligibility never expires, so a slot in which the sequencer produces no
// milestone inside the window only delays the transit.
//
// A transit's tag-along can be consumed only by the sequencer it is addressed
// to, so the candidates in one sequencer's backlog are only some of the
// competitors; the others come from the node's MineTransitRegistry. A
// sequencer consumes its own candidate only when no validated competitor
// addressed to an active sequencer ranks above it; otherwise it stands back
// and the best one's target includes it. Two sequencers then never include
// competing transits, and the winner is the one the rule names, whichever
// sequencer a miner addresses. If that target never takes it, the chain still
// moves: the next transit spends its mine output, so whoever includes the next
// one includes it too. See kb/mine_settlement_upgrade.md.

// mineSettlementWindowTicks is the width of the settlement window, counted back
// from the pre-branch consolidation zone. Wide enough for every sequencer pace
// to place at least one milestone in it. Shared with the miner, whose round
// for a slot ends where the window begins.
const mineSettlementWindowTicks = txbuildercore.MineSettlementWindowTicks

// mineTransit is what the rule reads off a mine transit.
type mineTransit struct {
	pred      base.OutputID // the mine output it spends: competitors share it
	slot      uint32
	vrfOutput []byte
	txid      base.TransactionID
	// target: the sequencer its tag-along is addressed to
	target base.ChainID
	// pending: constraints not validated yet, so the VRF output is unknown
	pending bool
}

// mineTransitOf reads the descriptor off the transaction that produced a
// tag-along candidate, or reports false for anything that is not a mine
// transit. A transit whose constraints are not validated yet is pending: the VRF
// output is decoded from the proof without verifying it, and a forged proof can
// be given any value, so it is neither ranked nor consumed but left for a later
// milestone. Validity is read off the constraints flag, not the Good status: a
// non-sequencer transaction is marked defined only in its attacher's past cone,
// never globally, so a status check would let every transit pass as an ordinary
// tag-along.
func mineTransitOf(vid *vertex.WrappedTx) (mineTransit, bool) {
	if vid == nil {
		return mineTransit{}, false
	}
	tx := vid.GetTransaction()
	if tx == nil {
		return mineTransit{}, false
	}
	m, ok := mineTransitOfTx(tx)
	if !ok {
		return m, false
	}
	if !vid.FlagsUp(vertex.FlagVertexConstraintsValid) && vid.GetTxStatus() != vertex.Good {
		return m, true
	}
	// the mine lock verifies the proof, so a validated transit always yields it
	if beta, ok := tx.MineVRFOutput(); ok {
		m.vrfOutput, m.pending = beta, false
	}
	return m, true
}

// mineTransitOfTx reads the descriptor of a mine transit, pending: nothing about
// its validity is known from the transaction alone.
func mineTransitOfTx(tx *transaction.Transaction) (mineTransit, bool) {
	if !tx.IsMiningTransaction() {
		return mineTransit{}, false
	}
	m := mineTransit{
		pred:    tx.MustInputAt(0),
		slot:    tx.Slot(),
		txid:    tx.ID(),
		pending: true,
	}
	for i := 0; i < tx.NumProducedOutputs(); i++ {
		if t := tx.MustProducedOutputAt(byte(i)).TagAlongLock(); t != nil {
			m.target = t.TargetSequencerID
			break
		}
	}
	return m, true
}

// betterThan is the canonical order among transits on one predecessor. It is
// the same order as the miner's (proxi/node_cmd/mine/mine_tree.go betterThan) once
// the chain height, equal on one predecessor, is taken out.
func (m *mineTransit) betterThan(other *mineTransit) bool {
	switch {
	case m.slot != other.slot:
		return m.slot < other.slot
	case !bytes.Equal(m.vrfOutput, other.vrfOutput):
		return bytes.Compare(m.vrfOutput, other.vrfOutput) < 0
	default:
		return bytes.Compare(m.txid[:], other.txid[:]) < 0
	}
}

// betterSolutionThan is the canonical order without the txid tie-break: transits
// carrying the same solution, sent to different sequencers, rank equal.
func (m *mineTransit) betterSolutionThan(other *mineTransit) bool {
	if m.slot != other.slot {
		return m.slot < other.slot
	}
	return bytes.Compare(m.vrfOutput, other.vrfOutput) < 0
}

// settled reports whether a transit at the given slot may be consumed by a
// milestone at targetTs: from the settlement window of its own slot on.
func (m *mineTransit) settled(targetTs base.LedgerTime, preBranchConsolidationTicks byte) bool {
	if targetTs.Slot != m.slot {
		return targetTs.Slot > m.slot
	}
	return int(targetTs.Tick) >= MineSettlementTick(preBranchConsolidationTicks)
}

// settleMineTransits applies the rule to the tag-along candidates: of the mine
// transits sharing a predecessor only the canonical winner stays, only once it
// is settled, and only when none of its rivals, the validated competitors held
// outside this backlog, ranks above it. A pending transit is held. Everything
// that is not a mine transit passes through.
func settleMineTransits(outs []*_inputCandidate, targetTs base.LedgerTime, preBranchConsolidationTicks byte, rivals func(c *_inputCandidate) []mineTransit) []*_inputCandidate {
	winners := make(map[base.OutputID]*_inputCandidate)
	for _, c := range outs {
		if c.mine == nil || c.mine.pending {
			continue
		}
		if w, ok := winners[c.mine.pred]; !ok || c.mine.betterThan(w.mine) {
			winners[c.mine.pred] = c
		}
	}
	for pred, w := range winners {
		for _, r := range rivals(w) {
			if r.betterThan(w.mine) {
				delete(winners, pred)
				break
			}
		}
	}
	ret := outs[:0]
	for _, c := range outs {
		if c.mine == nil {
			ret = append(ret, c)
			continue
		}
		if winners[c.mine.pred] == c && c.mine.settled(targetTs, preBranchConsolidationTicks) {
			ret = append(ret, c)
		}
	}
	return ret
}

// mineTransitDescriptor fills the descriptor of a candidate produced by a mine
// transit, so the transaction is unwrapped once per candidate.
func mineTransitDescriptor(c *_inputCandidate) {
	if m, ok := mineTransitOf(c.wOut.VID); ok {
		c.mine = &m
	}
}

// activeSequencers is the active set as of the proposal's target slot.
func (p *proposal) activeSequencers() func(seqID base.ChainID) bool {
	return ActiveSequencersAt(p.taskData.targetTs.Slot, p.LatestMilestonesDescending)
}
