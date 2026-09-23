package task

import (
	"bytes"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
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
}

// mineTransitOf reads the descriptor off the transaction that produced a
// tag-along candidate, or reports false for anything that is not a fully
// validated mine transit. Full validation matters: the VRF output is decoded
// from the proof without verifying it, and a forged proof can be given any
// value, so an unvalidated candidate is not ranked but left for a later
// milestone.
func mineTransitOf(vid *vertex.WrappedTx) (mineTransit, bool) {
	if vid == nil || vid.GetTxStatus() != vertex.Good {
		return mineTransit{}, false
	}
	tx := vid.GetTransaction()
	if tx == nil || !tx.IsMiningTransaction() {
		return mineTransit{}, false
	}
	beta, ok := tx.MineVRFOutput()
	if !ok {
		return mineTransit{}, false
	}
	return mineTransit{
		pred:      tx.MustInputAt(0),
		slot:      tx.Slot(),
		vrfOutput: beta,
		txid:      tx.ID(),
	}, true
}

// betterThan is the canonical order among transits on one predecessor. It is
// the same order as the miner's (proxi/node_cmd/mine_tree.go betterThan) once
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

// settled reports whether a transit at the given slot may be consumed by a
// milestone at targetTs: from the settlement window of its own slot on.
func (m *mineTransit) settled(targetTs base.LedgerTime, preBranchConsolidationTicks byte) bool {
	if targetTs.Slot != m.slot {
		return targetTs.Slot > m.slot
	}
	return int(targetTs.Tick) >= int(base.MaxTickValue)-int(preBranchConsolidationTicks)-mineSettlementWindowTicks
}

// settleMineTransits applies the rule to the tag-along candidates: of the mine
// transits sharing a predecessor only the canonical winner stays, and only once
// it is settled. Everything that is not a mine transit passes through.
func settleMineTransits(outs []*_inputCandidate, targetTs base.LedgerTime, preBranchConsolidationTicks byte) []*_inputCandidate {
	winners := make(map[base.OutputID]*_inputCandidate)
	for _, c := range outs {
		if c.mine == nil {
			continue
		}
		if w, ok := winners[c.mine.pred]; !ok || c.mine.betterThan(w.mine) {
			winners[c.mine.pred] = c
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
