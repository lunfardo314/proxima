package task

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/mine"
)

// settleMineTransits applies the rule to the tag-along candidates: of the mine
// transits sharing a predecessor only the canonical winner stays, only once it
// is settled, and only when none of its rivals, the validated competitors held
// outside this backlog, ranks above it. A pending transit is held. Everything
// that is not a mine transit passes through.
func settleMineTransits(outs []*_inputCandidate, targetTs base.LedgerTime, preBranchConsolidationTicks byte, rivals func(c *_inputCandidate) []mine.Transit) []*_inputCandidate {
	winners := make(map[base.OutputID]*_inputCandidate)
	for _, c := range outs {
		if c.mine == nil || c.mine.Pending {
			continue
		}
		if w, ok := winners[c.mine.Pred]; !ok || c.mine.BetterThan(w.mine) {
			winners[c.mine.Pred] = c
		}
	}
	for pred, w := range winners {
		for _, r := range rivals(w) {
			if r.BetterThan(w.mine) {
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
		if winners[c.mine.Pred] == c && c.mine.Settled(targetTs, preBranchConsolidationTicks) {
			ret = append(ret, c)
		}
	}
	return ret
}

// mineTransitDescriptor fills the mine.Transit descriptor of a candidate produced by a mine
// transit, so the transaction is unwrapped once per candidate.
func mineTransitDescriptor(c *_inputCandidate) {
	if m, ok := mine.TransitOf(c.wOut.VID); ok {
		c.mine = &m
	}
}

// activeSequencers is the active set as of the proposal's target slot.
func (p *proposal) activeSequencers() func(seqID base.ChainID) bool {
	return mine.ActiveSequencersAt(p.taskData.targetTs.Slot, p.LatestMilestonesDescending)
}
