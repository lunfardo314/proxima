package task

import (
	"sort"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

const TraceTagZoneProposer = "propose-zone"

// tryZoneProposal builds a pre-branch zone milestone directly: it extends the own latest milestone and
// endorses peers' milestones of the slot, skipping those already in its past cone or conflicting with
// it. The zone milestone consumes nothing but its chain predecessor, so this is all it can fold in. It
// does not depend on the factory, whose skeletons late in the slot often extend an own milestone
// already superseded: sequencers whose final milestones see the same peers then hold the same past
// cone, and their branches tie (kb/library_upgrade7.md).
//
// Late in the slot the heaviest milestones hold nearly the same past cone, so endorsing them in order
// fills the endorsement limit with near-duplicates and leaves out every chain none of them folded in.
// What such a chain adds is chiefly its own chain output, consumed from the baseline state by its
// first milestone of the slot. So one heaviest milestone is endorsed first, for what the network
// holds in common, then the chains not yet in the past cone, heaviest output first, then the rest by
// weight. A chain whose latest milestone is refused, as when it forks and its later line carries a
// lineage that lost the branch race, is taken by its first milestone of the slot.
func (t *taskData) tryZoneProposal() *finalProposal {
	lib := ledger.L(t.targetTs.Slot)
	if !lib.IsPreBranchConsolidationTimestamp(t.targetTs) {
		return nil
	}
	extend := t.OwnLatestMilestoneOutput()
	if extend.VID == nil || extend.Slot() != t.targetTs.Slot || !ledger.ValidSequencerPace(extend.Timestamp(), t.targetTs) {
		return nil
	}
	a, err := attacher.NewIncrementalAttacher(t.Name, t.environment, t.targetTs, extend)
	if err != nil {
		t.Tracef(TraceTagZoneProposer, "tryZoneProposal %s: can't create attacher: '%v'", t.Name, err)
		return nil
	}
	// endorsements stop two ticks before the build deadline, leaving finalize (coverage, the
	// transaction) the time it needs: on a network of many peers the endorsements alone can fill
	// the final's build window, and the coverage of a final's past cone can take well over a tick
	stopAt, bounded := t.ctx.Deadline()
	stopAt = stopAt.Add(-2 * ledger.TickDuration())
	full := func() bool {
		if uint64(len(a.Endorsing())) >= lib.MaxNumberOfEndorsements {
			return true
		}
		if bounded && time.Now().After(stopAt) {
			t.slotData.Count("zoneEndorseCut")
			return true
		}
		return false
	}
	insert := func(peer *vertex.WrappedTx) bool {
		if err := a.InsertEndorsement(peer); err != nil {
			t.Tracef(TraceTagZoneProposer, "tryZoneProposal %s: endorsement %s skipped: %v", t.Name, peer.IDShortString(), err)
			return false
		}
		return true
	}
	candidates := t.Backlog().CandidatesToEndorseSorted(t.targetTs)

	for _, peer := range candidates {
		if full() || insert(peer) {
			break
		}
	}

	type chainLine struct {
		line   []*vertex.WrappedTx // latest first
		weight uint64
	}
	var outside []chainLine
	for _, peer := range candidates {
		if line := slotLine(peer, t.targetTs.Slot); !folded(a, extend.VID, line) {
			outside = append(outside, chainLine{line: line, weight: sequencerOutputCoverage(peer, t.targetTs)})
		}
	}
	sort.SliceStable(outside, func(i, j int) bool { return outside[i].weight > outside[j].weight })
	for _, c := range outside {
		if full() {
			break
		}
		if folded(a, extend.VID, c.line) {
			continue // folded in by a milestone endorsed in this loop
		}
		// the latest milestone, else the first of the slot: a later one refused usually carries what got
		// the latest refused, and every try is a conflict check over the past cone
		switch first := c.line[len(c.line)-1]; {
		case insert(c.line[0]):
			t.slotData.Count("zoneOutsideChain")
		case first != c.line[0] && insert(first):
			t.slotData.Count("zoneOutsideChain")
			t.slotData.Count("zoneOutsideChainEarlier")
		default:
			t.slotData.Count("zoneOutsideRefused")
		}
	}

	for _, peer := range candidates {
		if full() {
			break
		}
		insert(peer)
	}

	if len(a.Endorsing()) == 0 || !a.Completed() {
		a.Close()
		return nil
	}
	prop, err := t.newProposal(a)
	if err != nil {
		t.Tracef(TraceTagZoneProposer, "tryZoneProposal %s: can't create proposal: '%v'", t.Name, err)
		return nil
	}
	fp, err := prop.finalize("zone")
	if err != nil {
		t.logFinalizeFailure("tryZoneProposal-"+t.Name, err)
		return nil
	}
	return fp
}

// slotLine is a milestone and its chain predecessors of the same slot, latest first. The line stops
// at a branch or a vertex without a baseline, neither of which a zone milestone can endorse.
func slotLine(vid *vertex.WrappedTx, slot uint32) []*vertex.WrappedTx {
	ret := []*vertex.WrappedTx{vid}
	noReattach := func(base.TransactionID) *vertex.WrappedTx { return nil }
	for pred := vid.SequencerPredecessor(noReattach); pred != nil && pred.Slot() == slot && !pred.IsBranchTransaction(); pred = pred.SequencerPredecessor(noReattach) {
		if _, ok := pred.BaselineBranch(); !ok {
			break
		}
		ret = append(ret, pred)
	}
	return ret
}

// folded tells whether the attacher's past cone holds a milestone of the line: then the chain's
// output from the baseline state is already consumed in it.
func folded(a *attacher.IncrementalAttacher, extended *vertex.WrappedTx, line []*vertex.WrappedTx) bool {
	holders := append([]*vertex.WrappedTx{extended}, a.Endorsing()...)
	for _, m := range line {
		for _, h := range holders {
			if h == m || h.PastConeReferences(m) {
				return true
			}
		}
	}
	return false
}

// sequencerOutputCoverage is the coverage of a milestone's sequencer output: about what its chain
// adds to a past cone that does not hold it yet.
func sequencerOutputCoverage(vid *vertex.WrappedTx, ts base.LedgerTime) uint64 {
	wOut := vid.SequencerWrappedOutput()
	o := wOut.Output()
	if o == nil {
		return 0
	}
	return ledger.Coverage(o, wOut.DecodeID(), ts)
}
