package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
)

// onMilestoneConfirmed is the shared bookkeeping called when a milestone appears in the tippool.
func (seq *Sequencer) onMilestoneConfirmed(vid *vertex.WrappedTx) {
	seq.clearPendingSubmitIfMatch(vid.ID())
	// Anchor the pulse to the moment of tippool observation.
	// Updating from a stale value to a fresh one is always monotonic forward;
	// the next pulse will fire pulseInterval after this.
	seq.loopMu.Lock()
	seq.lastPulseAnchor = time.Now()
	if vid.IsBranchTransaction() {
		seq.branchCount++
	}
	slotData := seq.slotData
	seq.loopMu.Unlock()

	seq.AddOwnMilestone(vid)
	// record this milestone's own freeze / unfreeze transitions tentatively in the
	// delegation pool. The build loop is gated on pendingSubmit, so the next
	// proposal already waits for this confirmation before reading the pool.
	if seq.delegationPool != nil {
		seq.delegationPool.ApplyMilestone(vid)
	}
	seq.milestoneCount++
	// SlotData is internally locked; record outside loopMu.
	if vid.IsBranchTransaction() {
		if slotData != nil {
			slotData.BranchTxSubmitted(vid.ID())
		}
	} else {
		if slotData != nil {
			slotData.SequencerTxSubmitted(vid.ID())
		}
	}
	seq.updateInfo(vid)
	seq.runOnMilestoneSubmitted(vid)
	seq.onMilestoneSubmittedMetrics(vid)
}
