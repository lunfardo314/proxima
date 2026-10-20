package task

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// Endorsement policy of the canonical winner rule (kb/mine_settlement_upgrade.md).
//
// The settlement rule makes a compliant sequencer stand back when the best
// transit is addressed to someone else. Left at that, it then endorses by
// coverage whatever milestone is already there, which is the one of a sequencer
// that took a transit early or took a worse one: the compliant sequencers'
// own deference hands it the slot. So a compliant sequencer does not endorse a
// deviating inclusion, nor a milestone of the same slot built on one. The
// deviator keeps only its own coverage, and the best transit's lineage wins.
//
// A deviating inclusion is a milestone that consumes, as a direct input, an
// output of a mine transit of its own slot, either before the settlement window
// or when a better validated rival, addressed to an active sequencer, was known
// to this node before the window by a margin. The margin keeps a sequencer from
// being refused for missing a rival that reached it late.
//
// The refusal is confined to the slot. In the next slot the lineages are kept
// apart by the mine output itself: once the best transit is in the honest
// lineage, a milestone descending from the deviating one double-spends against
// it, and attachment refuses it like any conflict.

// mineDeviationMarginTicks is how long before the settlement tick a rival must
// have been registered to count against another sequencer's inclusion
const mineDeviationMarginTicks = 10

// MineSettlementTick is the first tick of a slot from which its transits may be consumed
func MineSettlementTick(preBranchConsolidationTicks byte) int {
	return int(base.MaxTickValue) - int(preBranchConsolidationTicks) - mineSettlementWindowTicks
}

// MineDeviation describes a deviating inclusion: the milestone, the transit it took, and
// whether it took it before the settlement window (otherwise: a better rival was known)
type MineDeviation struct {
	Milestone base.TransactionID
	Transit   base.TransactionID
	Early     bool
}

// deviatingInclusion reports how a milestone at consumerTs consuming an output of mine
// transit tx breaks the canonical winner rule, if it does: early (before the settlement
// window), or not the best (betterRival reports a validated rival ranking above the
// transit, addressed to an active sequencer, registered before the given time). The
// transit is in a validated milestone's past cone, so its VRF output is taken from the
// proof without verifying it again.
func deviatingInclusion(consumerTs base.LedgerTime, tx *transaction.Transaction, betterRival func(m mineTransit, before base.LedgerTime) bool, preBranchConsolidationTicks byte) (deviates, early bool) {
	m, ok := mineTransitOfTx(tx)
	if !ok || m.slot != consumerTs.Slot {
		// consumed in a later slot: eligibility never expires
		return false, false
	}
	if !m.settled(consumerTs, preBranchConsolidationTicks) {
		return true, true
	}
	beta, ok := tx.MineVRFOutput()
	if !ok {
		return false, false
	}
	m.vrfOutput, m.pending = beta, false
	deadline := base.T(m.slot, byte(MineSettlementTick(preBranchConsolidationTicks)-mineDeviationMarginTicks))
	return betterRival(m, deadline), false
}

// DeviatesFromMineRule returns the deviating inclusion found in milestone ms or in a
// milestone of its slot in its past cone, or nil. The walk stays within the slot and
// among sequencer milestones, a few dozen vertices.
func DeviatesFromMineRule(ms *vertex.WrappedTx, registry *MineTransitRegistry, active func(seqID base.ChainID) bool, preBranchConsolidationTicks byte) *MineDeviation {
	slot := ms.Slot()
	betterRival := func(m mineTransit, before base.LedgerTime) bool {
		return registry.BetterRivalKnownBefore(m, before, active)
	}
	visited := map[*vertex.WrappedTx]struct{}{ms: {}}
	queue := []*vertex.WrappedTx{ms}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		// collect under the vertex lock, inspect after: inspecting locks other vertices
		var deps []*vertex.WrappedTx
		cur.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			deps = make([]*vertex.WrappedTx, 0, len(v.Inputs)+len(v.Endorsements))
			deps = append(deps, v.Inputs...)
			deps = append(deps, v.Endorsements...)
		}})
		for _, dep := range deps {
			if dep == nil || dep.Slot() != slot {
				continue
			}
			if _, already := visited[dep]; already {
				continue
			}
			visited[dep] = struct{}{}
			if dep.IsSequencerTransaction() {
				queue = append(queue, dep)
				continue
			}
			tx := dep.GetTransaction()
			if tx == nil || !tx.IsMiningTransaction() {
				continue
			}
			if deviates, early := deviatingInclusion(cur.Timestamp(), tx, betterRival, preBranchConsolidationTicks); deviates {
				return &MineDeviation{Milestone: cur.ID(), Transit: tx.ID(), Early: early}
			}
		}
	}
	return nil
}

// ActiveSequencersAt reports whether a sequencer's latest milestone in the tippool is at
// most txbuildercore.ActiveSequencerSlots slots older than the target slot, the wallet
// rating's notion of active. A transit addressed to a sequencer that is not active is not
// deferred to: nobody may be there to include it.
func ActiveSequencersAt(targetSlot uint32, latestMilestones func(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx) func(seqID base.ChainID) bool {
	active := make(map[base.ChainID]struct{})
	latestMilestones(func(seqID base.ChainID, vid *vertex.WrappedTx) bool {
		if vid.Slot()+txbuildercore.ActiveSequencerSlots >= targetSlot {
			active[seqID] = struct{}{}
		}
		return false
	})
	return func(seqID base.ChainID) bool {
		_, ok := active[seqID]
		return ok
	}
}
