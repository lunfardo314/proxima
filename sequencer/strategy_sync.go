package sequencer

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

// syncStrategy implements the original synchronous sequencer approach:
// after submitting a milestone, it blocks polling the tippool until the milestone appears.
type syncStrategy struct {
	seq *Sequencer
}

func newSyncStrategy(seq *Sequencer) *syncStrategy {
	return &syncStrategy{seq: seq}
}

func (s *syncStrategy) start() {
	// no background goroutines needed
}

func (s *syncStrategy) getNextTargetTime() (base.LedgerTime, bool) {
	seq := s.seq

	if !seq.ClockCatchUpWithLedgerTime(seq.lastSubmittedTs) {
		return base.NilLedgerTime, false
	}

	nowis := ledger.TimeNow()

	nextBoundarySlot := nowis.NextSlotBoundary().Slot
	libNextSlot := ledger.L(nextBoundarySlot)
	if base.DiffTicks(nowis.NextSlotBoundary(), nowis) < int64(libNextSlot.PreBranchConsolidationTicks) {
		return nowis.NextSlotBoundary(), true
	}

	var targetAbsoluteMinimum base.LedgerTime

	if seq.lastSubmittedTs.IsSlotBoundary() {
		targetAbsoluteMinimum = seq.lastSubmittedTs.AddTicks(int(libNextSlot.PostBranchConsolidationTicks))
	} else {
		targetAbsoluteMinimum = base.MaximumTime(
			seq.lastSubmittedTs.AddTicks(seq.config.Pace),
			nowis.AddTicks(1),
		)
	}
	if uint8(targetAbsoluteMinimum.Tick) < libNextSlot.PostBranchConsolidationTicks {
		targetAbsoluteMinimum = base.T(targetAbsoluteMinimum.Slot, libNextSlot.PostBranchConsolidationTicks)
	}
	nextSlotBoundary := nowis.NextSlotBoundary()

	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum, true
	}
	minimumTicksAheadFromNow := (seq.config.Pace * 2) / 3
	targetAbsoluteMinimum = base.MaximumTime(targetAbsoluteMinimum, nowis.AddTicks(minimumTicksAheadFromNow))
	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum, true
	}

	if targetAbsoluteMinimum.TicksToNextSlotBoundary() <= seq.config.Pace {
		return base.MaximumTime(nextSlotBoundary, targetAbsoluteMinimum), true
	}

	return targetAbsoluteMinimum, true
}

const submitTimeout = 2 * time.Second

func (s *syncStrategy) submit(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata, targetTs base.LedgerTime) {
	seq := s.seq

	if !seq.decideSubmitMilestone(tx, meta) {
		seq.lastSubmittedTs = targetTs
		return
	}

	seq.OwnSequencerMilestoneIn(tx.Bytes(), meta, tx.ID())

	vid, err := s.waitMilestoneInTippool(tx.ID(), time.Now().Add(submitTimeout))
	if err != nil {
		seq.Log().Error(err)
		seq.lastSubmittedTs = targetTs
		return
	}
	seq.lastSubmittedTs = vid.Timestamp()
	seq.onMilestoneConfirmed(vid)

	if targetTs.IsSlotBoundary() {
		seq.Log().Infof("SLOT STATS: %s", seq.slotData.Lines().Join(", "))
	}
}

// waitMilestoneInTippool polls the tippool until the submitted milestone appears or deadline expires.
func (s *syncStrategy) waitMilestoneInTippool(txid base.TransactionID, deadline time.Time) (*vertex.WrappedTx, error) {
	seq := s.seq
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-seq.Ctx().Done():
			return nil, fmt.Errorf("waitMilestoneInTippool: %s has been cancelled", txid.StringShort())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("waitMilestoneInTippool: deadline %v has been missed while waiting for %s in the tippool. hex=%s",
					deadline, txid.StringShort(), txid.StringHex())
			}
			vid := seq.GetLatestMilestone(seq.sequencerID)
			if vid != nil && vid.ID() == txid {
				return vid, nil
			}
		}
	}
}

var _ sequencerStrategy = (*syncStrategy)(nil)
