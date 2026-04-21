package sequencer

import (
	"errors"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/task"
	"github.com/lunfardo314/proxima/util"
)

const (
	// milestoneWatchInterval is how often the background watcher polls the tippool.
	milestoneWatchInterval = 20 * time.Millisecond

	// selfAttachmentLatencyToleranceTicks is the maximum wall-clock latency (in ticks)
	// between fire-and-forget submission of an own milestone and its appearance in the
	// tippool. If exceeded, the sequencer throttles: it stops issuing new milestones
	// until either the pending one is confirmed or the next slot's post-branch
	// consolidation zone is reached. Prevents the submit-faster-than-attach spiral.
	selfAttachmentLatencyToleranceTicks = 12

	// TraceTagSeqPolicy gates the pulse-policy trace lines (pulse waits, attempts,
	// zone C, throttled). Enable via logger.traceTags in the node config.
	TraceTagSeqPolicy = "seq_policy"
)

// isOverloaded reports whether the last submitted own milestone has not appeared
// in the tippool within selfAttachmentLatencyToleranceTicks. Returns elapsed wall-clock
// since submission and the pending status snapshot when overloaded.
func (seq *Sequencer) isOverloaded() (bool, time.Duration, pendingSubmitStatus) {
	seq.pendingSubmitMu.Lock()
	defer seq.pendingSubmitMu.Unlock()
	if !seq.pendingSubmit.awaiting {
		return false, 0, pendingSubmitStatus{}
	}
	elapsed := time.Since(seq.pendingSubmit.since)
	tolerance := time.Duration(selfAttachmentLatencyToleranceTicks) * ledger.TickDuration()
	if elapsed <= tolerance {
		return false, elapsed, seq.pendingSubmit
	}
	return true, elapsed, seq.pendingSubmit
}

// recordPendingSubmit marks a milestone as awaiting self-attachment confirmation.
// Called from submitMilestone after the fire-and-forget send succeeds.
func (seq *Sequencer) recordPendingSubmit(txID base.TransactionID, ts base.LedgerTime) {
	seq.pendingSubmitMu.Lock()
	defer seq.pendingSubmitMu.Unlock()
	seq.pendingSubmit = pendingSubmitStatus{
		awaiting: true,
		since:    time.Now(),
		ts:       ts,
		txID:     txID,
	}
}

// clearPendingSubmitIfMatch clears the pending marker if the observed milestone's
// txID matches the pending one. Strict equality avoids false clears from stale
// milestones re-surfacing in the tippool.
func (seq *Sequencer) clearPendingSubmitIfMatch(txID base.TransactionID) {
	seq.pendingSubmitMu.Lock()
	defer seq.pendingSubmitMu.Unlock()
	if seq.pendingSubmit.awaiting && seq.pendingSubmit.txID == txID {
		seq.pendingSubmit = pendingSubmitStatus{}
	}
}

// clearPendingSubmit unconditionally clears the pending marker. Used as the
// slot-rollover escape hatch: if the pending milestone never attached and a
// fresh slot is past post-branch consolidation, stop waiting and resume from
// whatever chain tip is currently visible.
func (seq *Sequencer) clearPendingSubmit() {
	seq.pendingSubmitMu.Lock()
	defer seq.pendingSubmitMu.Unlock()
	seq.pendingSubmit = pendingSubmitStatus{}
}

// doSequencerSlot runs the sequencer loop for one slot (returns on branch submission or stop).
//
// Pulse-based reference policy:
//   - the sequencer emits at most one own milestone per pulse interval (sequencerPulseTicks);
//   - the pulse is anchored to the moment the previous own milestone became visible in the
//     local tippool (see strategy.go:onMilestoneConfirmed);
//   - within pulseInterval since the anchor, submissions are skipped;
//   - a pending in-flight submission gates the pulse (no new submission while our previous
//     one has not been observed) — the throttle is a second-line safety for stuck pending;
//   - at slot-edge entry to the pre-branch zone, the loop switches to branch submission.
//
// What the factory produces is taken as-is at pulse time (no plateau wait). Coverage
// improvements and backlog drain influence WHAT goes in, not WHETHER to submit.
//
// Returns false if the sequencer should stop.
func (seq *Sequencer) doSequencerSlot() bool {
	// pause during snapshot
	if seq.IsSnapshotting() {
		seq.log.Infof("sequencer paused: snapshot in progress")
		seq.RepeatSync(2*time.Second, func() bool {
			return seq.IsSnapshotting()
		})
		seq.log.Infof("sequencer resumed: snapshot finished")
	}

	if seq.config.MaxBranches != 0 && seq.branchCount >= seq.config.MaxBranches {
		seq.log.Infof("reached max limit of branch milestones %d -> stopping", seq.config.MaxBranches)
		return false
	}

	// wait for clock to catch up with last submission
	if !seq.ClockCatchUpWithLedgerTime(seq.lastSubmittedTs) {
		return false
	}

	tickDuration := ledger.TickDuration()
	// Pulse interval = cfg.Pace ticks of wall-clock, see claude/seq-improvements.md.
	// Default cfg.Pace = defaultSequencerPaceTicks (12); tests override via WithPace().
	pulseInterval := time.Duration(seq.config.Pace) * tickDuration

	ticker := time.NewTicker(tickDuration)
	defer ticker.Stop()

	for {
		select {
		case <-seq.Ctx().Done():
			return false
		case <-ticker.C:
		}

		if seq.Ctx().Err() != nil {
			return false
		}

		nowTs := ledger.TimeNow()
		nextBoundary := nowTs.NextSlotBoundary()
		lib := ledger.L(nextBoundary.Slot)
		currentSlot := nowTs.Slot

		// check for max target ts (testing only)
		if seq.config.MaxTargetTs != base.NilLedgerTime && nowTs.After(seq.config.MaxTargetTs) {
			seq.log.Infof("current time %s is after maximum ts %s -> stopping", nowTs, seq.config.MaxTargetTs)
			return false
		}

		// The factory targets the current slot. Non-branch milestones are built within the
		// current slot; the branch at the slot edge transitions to the next.
		if seq.skeletonFactory != nil {
			seq.skeletonFactory.SetTargetSlot(currentSlot)
		}

		// ensure slotData
		if seq.slotData == nil || seq.slotData.Slot() != currentSlot {
			seq.slotData = task.NewSlotData(currentSlot)
		}

		ticksToSlotEnd := base.DiffTicks(nextBoundary, nowTs)

		// --- Throttle check (stuck pending): if the last submitted own milestone has
		// not attached within tolerance, pause submissions. Escape when the pending
		// milestone is in a prior slot and we've entered the next slot's post-branch
		// consolidation zone (accept the loss, resume from whatever chain tip is visible).
		// NOTE: under normal operation the pending.awaiting gate below also blocks the
		// pulse. This check exists only to log and to escape the stuck case.
		if overloaded, elapsed, pending := seq.isOverloaded(); overloaded {
			if nowTs.Slot > pending.ts.Slot && nowTs.Tick >= lib.PostBranchConsolidationTicks {
				seq.clearPendingSubmit()
			} else {
				if seq.lastOverloadLogSlot != nowTs.Slot {
					tolerance := time.Duration(selfAttachmentLatencyToleranceTicks) * ledger.TickDuration()
					seq.Log().Warnf("sequencer throttled: self-attachment latency %v exceeds tolerance %v (pending %s)",
						elapsed, tolerance, pending.txID.StringShort())
					seq.Tracef(TraceTagSeqPolicy, "throttled: elapsed=%v tolerance=%v pending=%s",
						elapsed, tolerance, pending.txID.StringShort())
					seq.lastOverloadLogSlot = nowTs.Slot
				}
				continue
			}
		}

		// --- Branch time: within PreBranchConsolidationTicks of slot edge ---
		// Zone C is branch-only in this reference; non-branch pulses are suppressed here.
		// The rare tick-126 + tick-0 two-tx play (see claude/seq-improvements.md) is not
		// implemented in the reference policy.
		if ticksToSlotEnd < int64(lib.PreBranchConsolidationTicks) {
			seq.Tracef(TraceTagSeqPolicy, "zone C: submitting branch at %s", nextBoundary)
			return seq.generateAndSubmitBranch(nextBoundary)
		}

		// --- Pulse gate (primary): previous own milestone not yet observed in tippool. ---
		seq.pendingSubmitMu.Lock()
		awaiting := seq.pendingSubmit.awaiting
		seq.pendingSubmitMu.Unlock()
		if awaiting {
			continue
		}

		// --- Pulse gate (timing): pulseInterval must have elapsed since anchor. ---
		elapsedSinceAnchor := time.Since(seq.lastPulseAnchor)
		if elapsedSinceAnchor < pulseInterval {
			continue
		}

		// Pulse fires: attempt build and submit. Advance the anchor regardless of outcome
		// so we don't rapid-fire after a failed attempt. On success, the tippool
		// observation of the new milestone will further advance the anchor.
		fired := seq.tryBuildAndSubmit()
		seq.lastPulseAnchor = time.Now()
		seq.Tracef(TraceTagSeqPolicy, "pulse fired: elapsed=%v built=%v", elapsedSinceAnchor, fired)
	}
}

// tryBuildAndSubmit builds a milestone via task.Run (which inserts tag-alongs and freezes
// on top of the skeleton) and submits it. Returns true on successful submission.
//
// Target timestamp = max(nowTs, paceMin, T(slot, PostBranchConsolidationTicks)).
//
//   - paceMin = lastSubmittedTs + TransactionPaceSequencer (ledger-enforced in parse.go).
//   - PostBranchConsolidationTicks floor is still required by the EasyFL constraint
//     checkPostBranchConsolidationTicks in ledger/def/sequencer.easyfl. It will be removed
//     when the ledger-side refactor ships with the next testnet reset; until then, the
//     Go sequencer must keep producing timestamps that satisfy it.
//
// The pulse cadence (doSequencerSlot) already spaces these attempts ~1 s apart, so nowTs
// is a good enough target — no separate look-ahead offset is needed.
func (seq *Sequencer) tryBuildAndSubmit() bool {
	nowTs := ledger.TimeNow()
	lib := ledger.L(nowTs.Slot)
	paceMin := seq.lastSubmittedTs.AddTicks(int(lib.TransactionPaceSequencer))
	pbcFloor := base.T(nowTs.Slot, lib.PostBranchConsolidationTicks)

	targetTs := base.MaximumTime(nowTs, paceMin, pbcFloor)

	// don't overshoot into next slot
	nextBoundary := nowTs.NextSlotBoundary()
	if !targetTs.Before(nextBoundary) {
		return false
	}

	// must not be a slot boundary (branches handled separately)
	if targetTs.IsSlotBoundary() {
		return false
	}

	if !ledger.ValidSequencerPace(seq.lastSubmittedTs, targetTs) {
		return false
	}

	seq.newTargetSet()
	seq.slotData.NewTarget()

	msTx, meta, _, err := seq.generateMilestoneForTarget(targetTs)

	switch {
	case errors.Is(err, task.ErrNotGoodEnough):
		seq.slotData.NotGoodEnough()
		return false
	case errors.Is(err, task.ErrNoProposals):
		seq.slotData.NoProposals()
		return false
	case err != nil:
		return false
	}
	util.Assertf(msTx != nil, "msTx != nil")

	meta.TxBytesReceived = util.Ref(time.Now())
	seq.submitMilestone(msTx, meta, targetTs)
	seq.adjustBudget(true)
	return true
}

// generateAndSubmitBranch generates and submits a branch transaction for the slot boundary.
// Does NOT wait for the boundary — the boundary time serves as the context deadline,
// giving ~PreBranchConsolidationTicks of time for branch generation.
// Returns true to continue the sequencer loop, false to stop.
func (seq *Sequencer) generateAndSubmitBranch(branchTs base.LedgerTime) bool {
	if seq.config.MaxTargetTs != base.NilLedgerTime && branchTs.After(seq.config.MaxTargetTs) {
		seq.log.Infof("branch target %s is after maximum ts %s -> stopping", branchTs, seq.config.MaxTargetTs)
		return false
	}

	seq.newTargetSet()
	if seq.slotData == nil {
		seq.slotData = task.NewSlotData(branchTs.Slot)
	}
	seq.slotData.NewTarget()

	msTx, meta, _, err := seq.generateMilestoneForTarget(branchTs)

	switch {
	case errors.Is(err, task.ErrNotGoodEnough):
		seq.slotData.NotGoodEnough()
	case errors.Is(err, task.ErrNoProposals):
		seq.slotData.NoProposals()
		seq.adjustBudget(false)
	case err != nil:
		seq.adjustBudget(false)
		seq.Log().Warnf("branch generation: %v (budget: %d/%d)", err, seq.budgetLevel, maxBudgetLevel)
	default:
		util.Assertf(msTx != nil, "msTx != nil")
		meta.TxBytesReceived = util.Ref(time.Now())
		seq.submitMilestone(msTx, meta, branchTs)
		seq.adjustBudget(true)
	}

	seq.Log().Infof("SLOT STATS: %s, budget: %d/%d", seq.slotData.Lines().Join(", "), seq.budgetLevel, maxBudgetLevel)
	seq.slotData = nil

	// advance lastSubmittedTs past the branch boundary even on failure,
	// so the next doSequencerSlot iteration starts at the next slot, not this one
	if branchTs.After(seq.lastSubmittedTs) {
		seq.lastSubmittedTs = branchTs
	}
	return true
}

// submitMilestone sends a milestone to the network fire-and-forget and advances lastSubmittedTs optimistically.
func (seq *Sequencer) submitMilestone(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata, targetTs base.LedgerTime) {
	if !seq.decideSubmitMilestone(tx, meta) {
		seq.lastSubmittedTs = targetTs
		return
	}

	// fire-and-forget: send to input queue and advance optimistically
	seq.OwnSequencerMilestoneIn(tx.Bytes(), meta, tx.ID())
	seq.lastSubmittedTs = tx.Timestamp()
	seq.recordPendingSubmit(tx.ID(), tx.Timestamp())
}

// milestoneWatcher polls the tippool for own milestones and calls onMilestoneConfirmed.
func (seq *Sequencer) milestoneWatcher() {
	ticker := time.NewTicker(milestoneWatchInterval)
	defer ticker.Stop()

	var lastSeen base.TransactionID
	for {
		select {
		case <-seq.Ctx().Done():
			return
		case <-ticker.C:
		}
		vid := seq.GetLatestMilestone(seq.sequencerID)
		if vid == nil || vid.ID() == lastSeen {
			continue
		}
		lastSeen = vid.ID()
		seq.onMilestoneConfirmed(vid)
	}
}
