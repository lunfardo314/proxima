// Package mine is the canonical winner rule for the mine chain (kb/mine_conflict_rule.md,
// kb/mine_settlement_upgrade.md), shared by every sequencer version so that all of them
// treat miners the same way.
package mine

import (
	"bytes"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// The canonical winner rule.
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
// competitors; the others come from the node's Registry. A
// sequencer consumes its own candidate only when no validated competitor
// addressed to an active sequencer ranks above it; otherwise it stands back
// and the best one's target includes it. Two sequencers then never include
// competing transits, and the winner is the one the rule names, whichever
// sequencer a miner addresses. If that target never takes it, the chain still
// moves: the next transit spends its mine output, so whoever includes the next
// one includes it too. See kb/mine_settlement_upgrade.md.

// settlementWindowTicks is the width of the settlement window, counted back
// from the pre-branch consolidation zone. Wide enough for every sequencer pace
// to place at least one milestone in it. Shared with the miner, whose round
// for a slot ends where the window begins.
const settlementWindowTicks = txbuildercore.MineSettlementWindowTicks

// Transit is what the rule reads off a mine transit.
type Transit struct {
	Pred      base.OutputID // the mine output it spends: competitors share it
	Slot      uint32
	VRFOutput []byte
	TxID      base.TransactionID
	// Target: the sequencer its tag-along is addressed to
	Target base.ChainID
	// Pending: constraints not validated yet, so the VRF output is unknown
	Pending bool
}

// TransitOf reads the descriptor off the transaction that produced a
// tag-along candidate, or reports false for anything that is not a mine
// transit. A transit whose constraints are not validated yet is pending: the VRF
// output is decoded from the proof without verifying it, and a forged proof can
// be given any value, so it is neither ranked nor consumed but left for a later
// milestone. Validity is read off the constraints flag, not the Good status: a
// non-sequencer transaction is marked defined only in its attacher's past cone,
// never globally, so a status check would let every transit pass as an ordinary
// tag-along.
func TransitOf(vid *vertex.WrappedTx) (Transit, bool) {
	if vid == nil {
		return Transit{}, false
	}
	tx := vid.GetTransaction()
	if tx == nil {
		return Transit{}, false
	}
	m, ok := TransitOfTx(tx)
	if !ok {
		return m, false
	}
	if !vid.FlagsUp(vertex.FlagVertexConstraintsValid) && vid.GetTxStatus() != vertex.Good {
		return m, true
	}
	// the mine lock verifies the proof, so a validated transit always yields it
	if beta, ok := tx.MineVRFOutput(); ok {
		m.VRFOutput, m.Pending = beta, false
	}
	return m, true
}

// TransitOfTx reads the descriptor of a mine transit, pending: nothing about
// its validity is known from the transaction alone.
func TransitOfTx(tx *transaction.Transaction) (Transit, bool) {
	if !tx.IsMiningTransaction() {
		return Transit{}, false
	}
	m := Transit{
		Pred:    tx.MustInputAt(0),
		Slot:    tx.Slot(),
		TxID:    tx.ID(),
		Pending: true,
	}
	for i := 0; i < tx.NumProducedOutputs(); i++ {
		if t := tx.MustProducedOutputAt(byte(i)).TagAlongLock(); t != nil {
			m.Target = t.TargetSequencerID
			break
		}
	}
	return m, true
}

// betterThan is the canonical order among transits on one predecessor. It is
// the same order as the miner's (proxi/node_cmd/mine/mine_tree.go betterThan) once
// the chain height, equal on one predecessor, is taken out.
func (m *Transit) BetterThan(other *Transit) bool {
	switch {
	case m.Slot != other.Slot:
		return m.Slot < other.Slot
	case !bytes.Equal(m.VRFOutput, other.VRFOutput):
		return bytes.Compare(m.VRFOutput, other.VRFOutput) < 0
	default:
		return bytes.Compare(m.TxID[:], other.TxID[:]) < 0
	}
}

// betterSolutionThan is the canonical order without the txid tie-break: transits
// carrying the same solution, sent to different sequencers, rank equal.
func (m *Transit) betterSolutionThan(other *Transit) bool {
	if m.Slot != other.Slot {
		return m.Slot < other.Slot
	}
	return bytes.Compare(m.VRFOutput, other.VRFOutput) < 0
}

// settled reports whether a transit at the given slot may be consumed by a
// milestone at targetTs: from the settlement window of its own slot on.
func (m *Transit) Settled(targetTs base.LedgerTime, preBranchConsolidationTicks byte) bool {
	if targetTs.Slot != m.Slot {
		return targetTs.Slot > m.Slot
	}
	return int(targetTs.Tick) >= SettlementTick(preBranchConsolidationTicks)
}
