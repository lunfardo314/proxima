package task

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
)

const TraceTagZoneProposer = "propose-zone"

// tryZoneProposal builds a pre-branch zone milestone directly: it extends the own latest milestone and
// endorses the peers' latest milestones, heaviest first, skipping those already in its past cone or
// conflicting with it. The zone milestone consumes nothing but its chain predecessor, so this is all it
// can fold in. It does not depend on the factory, whose skeletons late in the slot often extend an own
// milestone already superseded: sequencers whose final milestones see the same peers then hold the same
// past cone, and their branches tie (kb/library_upgrade7.md).
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
	for _, peer := range t.Backlog().CandidatesToEndorseSorted(t.targetTs) {
		if uint64(len(a.Endorsing())) >= lib.MaxNumberOfEndorsements || t.ctx.Err() != nil {
			break
		}
		if err = a.InsertEndorsement(peer); err != nil {
			t.Tracef(TraceTagZoneProposer, "tryZoneProposal %s: endorsement %s skipped: %v", t.Name, peer.IDShortString(), err)
		}
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
