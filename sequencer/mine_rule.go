package sequencer

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/task"
)

// MineRuleFilter returns, for a target slot, whether a milestone may be endorsed under
// the canonical winner rule of mine transits: not when it, or a milestone of its slot it
// is built on, took a transit early or took one a better rival known in time should have
// won (task.DeviatesFromMineRule, kb/mine_settlement_upgrade.md).
func (seq *Sequencer) MineRuleFilter(targetSlot uint32) func(vid *vertex.WrappedTx) bool {
	preBranchTicks := ledger.L(targetSlot).PreBranchConsolidationTicks
	var active func(seqID base.ChainID) bool // read off the tippool only when a milestone is checked
	return func(vid *vertex.WrappedTx) bool {
		if active == nil {
			active = task.ActiveSequencersAt(targetSlot, seq.LatestMilestonesDescending)
		}
		d := task.DeviatesFromMineRule(vid, seq.mineTransits, active, preBranchTicks)
		if d == nil {
			return true
		}
		seq.evidenceMineRuleRefusal(vid, d)
		return false
	}
}

// evidenceMineRuleRefusal logs and counts a refused milestone once
func (seq *Sequencer) evidenceMineRuleRefusal(vid *vertex.WrappedTx, d *task.MineDeviation) {
	seq.mineRefusedMutex.Lock()
	if seq.mineRefused == nil || seq.mineRefusedSlot != vid.Slot() {
		seq.mineRefused = make(map[base.TransactionID]struct{})
		seq.mineRefusedSlot = vid.Slot()
	}
	_, already := seq.mineRefused[vid.ID()]
	seq.mineRefused[vid.ID()] = struct{}{}
	seq.mineRefusedMutex.Unlock()
	if already {
		return
	}
	how := "while a better transit was known"
	if d.Early {
		how = "before the settlement window"
	}
	seq.log.Infof("not endorsing %s: milestone %s took mine transit %s %s", vid.IDShortString(), d.Milestone.StringShort(), d.Transit.StringShort(), how)
	if seq.metrics != nil {
		seq.metrics.mineRuleRefused.Inc()
	}
}

// settlementPulseDue reports whether a milestone should be built now, ahead of the pulse,
// once per slot: the settlement window of the current slot has opened, no own milestone
// has been submitted in it yet, and the backlog holds a mine transit of the slot. The
// window is raced by whoever includes first, so the best transit's target must not wait
// out its pace.
func (seq *Sequencer) settlementPulseDue(nowTs base.LedgerTime, preBranchTicks byte) bool {
	settlementTs := base.T(nowTs.Slot, byte(task.MineSettlementTick(preBranchTicks)))
	if seq.settlementPulseSlot == nowTs.Slot || nowTs.Before(settlementTs) || !seq.lastSubmittedTs.Before(settlementTs) {
		return false
	}
	if !seq.backlog.HoldsMineTransitOfSlot(nowTs.Slot) {
		return false
	}
	seq.settlementPulseSlot = nowTs.Slot
	return true
}
