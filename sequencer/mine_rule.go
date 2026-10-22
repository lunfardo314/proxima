package sequencer

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/task"
)

// mineDeviationMemorySlots is how many slots after its own a deviating transit keeps its
// lineage from being endorsed. Long enough for the lineage holding the better transit to
// win the next branches; short enough that a sequencer left alone on the losing side rejoins.
const mineDeviationMemorySlots = 3

// MineRuleFilter returns, for a target slot, whether a milestone may be endorsed under
// the canonical winner rule of mine transits (kb/mine_settlement_upgrade.md). It only
// filters: what remains is tried in coverage order. A milestone is refused when it,
// or a milestone of its slot it is built on, took a transit against the rule
// (task.InspectMineInclusions), and, in the next slots, when its lineage holds such a
// transit while this sequencer's own lineage holds a better one on the same predecessor:
// otherwise the deviator's lineage, which wins nothing by coverage in its own slot, wins
// the next one by gathering the early milestones.
func (seq *Sequencer) MineRuleFilter(targetSlot uint32) func(vid *vertex.WrappedTx) bool {
	preBranchTicks := ledger.L(targetSlot).PreBranchConsolidationTicks
	var active func(seqID base.ChainID) bool // read off the tippool only when a milestone is checked
	var lineage *mineLineageCheck
	return func(vid *vertex.WrappedTx) bool {
		if active == nil {
			active = task.ActiveSequencersAt(targetSlot, seq.LatestMilestonesDescending)
			lineage = seq.newMineLineageCheck(targetSlot)
		}
		d := task.InspectMineInclusions(vid, seq.mineTransits, active, preBranchTicks)
		if d != nil {
			seq.rememberMineDeviation(d)
			seq.evidenceMineRuleRefusal(vid, d, false)
			return false
		}
		if d = lineage.deviation(vid); d != nil {
			seq.evidenceMineRuleRefusal(vid, d, true)
			return false
		}
		return true
	}
}

// rememberMineDeviation records a deviating transit for mineDeviationMemorySlots
func (seq *Sequencer) rememberMineDeviation(d *task.MineDeviation) {
	seq.mineRefusedMutex.Lock()
	defer seq.mineRefusedMutex.Unlock()

	if seq.mineDeviations == nil {
		seq.mineDeviations = make(map[base.TransactionID]task.MineDeviation)
	}
	seq.mineDeviations[d.Transit] = *d
	for txid, rec := range seq.mineDeviations {
		if rec.Slot+mineDeviationMemorySlots < d.Slot {
			delete(seq.mineDeviations, txid)
		}
	}
}

// mineLineageCheck answers, for the candidates of one target slot, whether a candidate's
// lineage holds a remembered deviating transit while the own lineage holds a better one.
// Branch lookups are cached for the lifetime of the check, one candidate list.
type mineLineageCheck struct {
	seq        *Sequencer
	recent     []task.MineDeviation
	ownBranch  base.TransactionID
	hasOwn     bool
	knownCache map[[2]base.TransactionID]bool
}

func (seq *Sequencer) newMineLineageCheck(targetSlot uint32) *mineLineageCheck {
	ret := &mineLineageCheck{seq: seq, knownCache: make(map[[2]base.TransactionID]bool)}
	seq.mineRefusedMutex.Lock()
	for _, rec := range seq.mineDeviations {
		if rec.Slot < targetSlot && rec.Slot+mineDeviationMemorySlots >= targetSlot {
			ret.recent = append(ret.recent, rec)
		}
	}
	seq.mineRefusedMutex.Unlock()
	if len(ret.recent) > 0 {
		if own := seq.GetLatestMilestone(seq.SequencerID()); own != nil {
			ret.ownBranch, ret.hasOwn = own.BaselineBranch()
		}
	}
	return ret
}

func (c *mineLineageCheck) knows(branchID, txid base.TransactionID) bool {
	key := [2]base.TransactionID{branchID, txid}
	if known, ok := c.knownCache[key]; ok {
		return known
	}
	known := c.seq.Branches().BranchKnowsTransaction(branchID, txid)
	c.knownCache[key] = known
	return known
}

// deviation returns the remembered deviating transit the candidate's lineage holds, if the
// own lineage holds another transit on the same predecessor instead; nil otherwise. When the
// own lineage spent nothing on that predecessor there is nothing better to keep, and joining
// the deviator's lineage is the only way forward.
func (c *mineLineageCheck) deviation(vid *vertex.WrappedTx) *task.MineDeviation {
	if len(c.recent) == 0 || !c.hasOwn {
		return nil
	}
	candBranch, ok := vid.BaselineBranch()
	if !ok || candBranch == c.ownBranch {
		return nil
	}
	for i := range c.recent {
		d := &c.recent[i]
		if !c.knows(candBranch, d.Transit) || c.knows(c.ownBranch, d.Transit) {
			continue
		}
		for _, other := range c.seq.mineTransits.TransitsOn(d.Pred) {
			if other != d.Transit && c.knows(c.ownBranch, other) {
				return d
			}
		}
	}
	return nil
}

// evidenceMineRuleRefusal logs and counts a refused milestone once
func (seq *Sequencer) evidenceMineRuleRefusal(vid *vertex.WrappedTx, d *task.MineDeviation, lineage bool) {
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
	if lineage {
		seq.log.Infof("not endorsing %s: its lineage holds mine transit %s of slot %d, taken by %s %s; ours holds a better one",
			vid.IDShortString(), d.Transit.StringShort(), d.Slot, d.Milestone.StringShort(), how)
	} else {
		seq.log.Infof("not endorsing %s: milestone %s took mine transit %s %s", vid.IDShortString(), d.Milestone.StringShort(), d.Transit.StringShort(), how)
	}
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
