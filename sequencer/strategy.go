package sequencer

import (
	"github.com/lunfardo314/proxima/core/vertex"
)

// onMilestoneConfirmed is the shared bookkeeping called when a milestone appears in the tippool.
func (seq *Sequencer) onMilestoneConfirmed(vid *vertex.WrappedTx) {
	seq.AddOwnMilestone(vid)
	seq.milestoneCount++
	if vid.IsBranchTransaction() {
		seq.branchCount++
		if seq.slotData != nil {
			seq.slotData.BranchTxSubmitted(vid.ID())
		}
	} else {
		if seq.slotData != nil {
			seq.slotData.SequencerTxSubmitted(vid.ID())
		}
	}
	seq.updateInfo(vid)
	seq.runOnMilestoneSubmitted(vid)
	seq.onMilestoneSubmittedMetrics(vid)
}
