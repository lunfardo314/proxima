package sequencer

import (
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

// sequencerStrategy abstracts the differences between sync and async sequencer approaches.
// The sync strategy blocks on tippool confirmation after each submission.
// The async strategy submits fire-and-forget and monitors the tippool in the background.
type sequencerStrategy interface {
	// start is called once before the main loop (e.g. to start background goroutines)
	start()
	// getNextTargetTime computes the next target timestamp for milestone generation
	getNextTargetTime() (base.LedgerTime, bool)
	// submit sends a milestone to the network and handles post-submission bookkeeping.
	// targetTs is the originally intended target (used for lastSubmittedTs advancement on failure).
	submit(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata, targetTs base.LedgerTime)
}

// onMilestoneConfirmed is the shared bookkeeping called when a milestone appears in the tippool.
// Both sync and async strategies call this with the confirmed milestone VID.
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
