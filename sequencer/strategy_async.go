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

	// lateBranchToleranceTicks is how far past its boundary a latched branch may still be
	// submitted. It absorbs scheduling jitter in the loop; beyond it the branch is stale enough
	// that the slot is better conceded than contested.
	lateBranchToleranceTicks = 4

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
//   - the normal coverage-maximizing pulse keeps running through the pre-branch consolidation
//     zone: there the builder makes the milestone endorse-only (1 input, see the ledger
//     PreBranchConsolidationTicks rule), so the sequencer keeps consolidating others' coverage
//     right up to the pace limit — the last milestone lands as close to the branch as sequencer
//     pace allows, capturing the maximal slot coverage delta;
//   - the branch is submitted once no further pace-feasible milestone fits before the slot edge,
//     extending the freshest own milestone.
//
// What the factory produces is taken as-is at pulse time (no plateau wait). Coverage
// improvements and backlog drain influence WHAT goes in, not WHETHER to submit.
//
// Returns false if the sequencer should stop.
func (seq *Sequencer) doSequencerSlot() bool {
	// pause during snapshot. Cancel the loop watchdog so it doesn't fire while we
	// intentionally wait for the snapshot to finish; the per-tick Check below
	// re-arms it once the inner loop starts running.
	if seq.IsSnapshotting() {
		seq.cancelLoopCheckpoint()
		seq.log.Infof("sequencer paused: snapshot in progress")
		seq.RepeatSync(2*time.Second, func() bool {
			return seq.IsSnapshotting()
		})
		seq.log.Infof("sequencer resumed: snapshot finished")
	}

	seq.loopMu.Lock()
	branchCount := seq.branchCount
	seq.loopMu.Unlock()
	if seq.config.MaxBranches != 0 && branchCount >= seq.config.MaxBranches {
		seq.log.Infof("reached max limit of branch milestones %d -> stopping", seq.config.MaxBranches)
		return false
	}

	// wait for clock to catch up with last submission. Same reasoning as above:
	// this is an intentional wait, not a stuck loop.
	seq.cancelLoopCheckpoint()
	if !seq.ClockCatchUpWithLedgerTime(seq.lastSubmittedTs) {
		return false
	}

	tickDuration := ledger.TickDuration()
	// Pulse interval = cfg.Pace ticks of wall-clock, see claude/seq-improvements.md.
	// Default cfg.Pace = defaultSequencerPaceTicks (12); tests override via WithPace().
	pulseInterval := time.Duration(seq.config.Pace) * tickDuration

	ticker := time.NewTicker(tickDuration)
	defer ticker.Stop()

	// Per-slot guard: the final pre-branch consolidation milestone is issued at most once per
	// slot. zoneSlot tracks the slot it applies to so it resets when the loop rolls into a new slot.
	finalConsolidationTried := false
	zoneSlot := uint32(0)

	// The boundary the pre-branch zone is working towards, latched when the zone opens.
	pendingBranch := base.NilLedgerTime

	for {
		select {
		case <-seq.Ctx().Done():
			return false
		case <-ticker.C:
		}

		if seq.Ctx().Err() != nil {
			return false
		}

		// Feed the loop watchdog once per tick. As long as the loop keeps ticking
		// it's by definition not stuck, regardless of whether the current ledger
		// slot completes (under load, throttle / awaiting gates can let the loop
		// span multiple slots before the branch-zone exit fires).
		seq.checkLoopCheckpoint()

		nowTs := ledger.TimeNow()

		// Submit the latched branch as soon as its boundary is reached, and keep submitting it
		// for a few ticks past it. A poll delayed beyond the boundary must not drop the branch:
		// NextSlotBoundary() returns the boundary itself only while standing exactly on it, so
		// one tick later the target moves to the next slot and the branch for this one is lost
		// with no trace. The zone leaves a single tick to catch — its polls are one tick apart
		// and the ticker's period is exactly one tick — so one late wake-up costs the slot.
		if pendingBranch != base.NilLedgerTime && !nowTs.Before(pendingBranch) {
			if base.DiffTicks(nowTs, pendingBranch) <= lateBranchToleranceTicks {
				return seq.generateAndSubmitBranch(pendingBranch)
			}
			// too stale: an old branch would only contest the slot it already lost
			pendingBranch = base.NilLedgerTime
		}

		nextBoundary := nowTs.NextSlotBoundary()
		lib := ledger.L(nextBoundary.Slot)
		currentSlot := nowTs.Slot

		// check for max target ts (testing only)
		if seq.config.MaxTargetTs != base.NilLedgerTime && nowTs.After(seq.config.MaxTargetTs) {
			seq.log.Infof("current time %s is after maximum ts %s -> stopping", nowTs, seq.config.MaxTargetTs)
			return false
		}

		// ensure slotData
		seq.loopMu.Lock()
		if seq.slotData == nil || seq.slotData.Slot() != currentSlot {
			seq.slotData = task.NewSlotData(currentSlot)
		}
		seq.loopMu.Unlock()

		if zoneSlot != currentSlot {
			zoneSlot = currentSlot
			finalConsolidationTried = false
			seq.coverageSafe = false
		}

		// The factory targets the current slot. Non-branch milestones are built within the
		// current slot; the branch at the slot edge transitions to the next.
		if seq.skeletonFactory != nil {
			// In no-branch mode, once the own milestone is safely referenced by a healthy peer,
			// stop feeding the factory so it idles (no more coverage-seeking skeletons) to save
			// CPU. Slot 0 means "unset". Placed AFTER the per-slot coverageSafe reset so the
			// factory is always active at the start of a slot — the first milestone always builds.
			if seq.noBranchMode() && seq.coverageSafe {
				seq.skeletonFactory.SetTargetSlot(0)
			} else {
				seq.skeletonFactory.SetTargetSlot(currentSlot)
			}
		}

		ticksToSlotEnd := base.DiffTicks(nextBoundary, nowTs)

		// --- Throttle check (stuck pending): if the last submitted own milestone has
		// not attached within tolerance, pause submissions. Escape when the pending
		// milestone is in a prior slot and we've crossed at least one sequencer pace
		// into the next slot (accept the loss, resume from whatever chain tip is visible).
		// NOTE: under normal operation the pending.awaiting gate below also blocks the
		// pulse. This check exists only to log and to escape the stuck case.
		if overloaded, elapsed, pending := seq.isOverloaded(); overloaded {
			if nowTs.Slot > pending.ts.Slot && nowTs.Tick >= lib.TransactionPaceSequencer {
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

		// --- Pre-branch consolidation zone ------------------------------------------------
		// Goal: drive competing branches toward EQUAL coverage delta, so the canonical winner
		// is decided by the fair, VRF-based branch inflation bonus rather than a consolidation-
		// timing edge. Equal coverage requires every sequencer's branch to have the same past
		// cone, which happens only if each consolidates the same fully-propagated tangle.
		//
		// So build ONE final coverage-maximizing milestone at the very last tick of the slot
		// (target = boundary - 1), built against the freshest tangle: it endorses every peer's
		// near-final milestone (endorsements need only 1-tick monotonicity, not pace), then the
		// branch extends it one tick later. The branch's chain-predecessor is exempt from the
		// sequencer pace constraint (ledger scanInputs), which is what lets this final milestone
		// land at the last tick with the branch immediately after. Regular pulses are held
		// through the zone.
		if seq.noBranchMode() {
			// No-branch mode: never issue a branch (never seek the branch inflation bonus).
			// Roll into the next slot at the boundary; the sequencer's milestones are carried
			// into committed state by other sequencers' branches. Coverage-seeking is
			// suppressed once its own milestone is healthy (see SuppressCoverageSeeking),
			// leaving only tag-along / delegation servicing through the normal pulse below.
			if ticksToSlotEnd <= 1 {
				return seq.rollSlotWithoutBranch(nextBoundary)
			}
		} else if ticksToSlotEnd < int64(lib.PreBranchConsolidationTicks) {
			pendingBranch = nextBoundary
			finalConsolidationTs := nextBoundary.AddTicks(-1)
			// hold until the final-consolidation tick, so no pulse lands between it and the zone
			// start and blocks the late target by pace
			if nowTs.Before(finalConsolidationTs) {
				continue
			}
			seq.pendingSubmitMu.Lock()
			awaiting := seq.pendingSubmit.awaiting
			seq.pendingSubmitMu.Unlock()
			// issue the final consolidation once, built now (as late as pace allows)
			if !finalConsolidationTried {
				finalConsolidationTried = true
				if !awaiting && seq.tryBuildAndSubmit() {
					seq.loopMu.Lock()
					seq.lastPulseAnchor = time.Now()
					seq.loopMu.Unlock()
					continue
				}
			}
			// wait for the final consolidation to attach so the branch extends it; hard fallback
			// at the very slot edge so a branch is never missed
			if awaiting && ticksToSlotEnd > 1 {
				continue
			}
			seq.Tracef(TraceTagSeqPolicy, "branch time: submitting branch at %s", nextBoundary)
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
		seq.loopMu.Lock()
		elapsedSinceAnchor := time.Since(seq.lastPulseAnchor)
		seq.loopMu.Unlock()
		if elapsedSinceAnchor < pulseInterval {
			continue
		}

		// Pulse fires: attempt build and submit. Advance the anchor regardless of outcome
		// so we don't rapid-fire after a failed attempt. On success, the tippool
		// observation of the new milestone will further advance the anchor.
		fired := seq.tryBuildAndSubmit()
		seq.loopMu.Lock()
		seq.lastPulseAnchor = time.Now()
		seq.loopMu.Unlock()
		seq.Tracef(TraceTagSeqPolicy, "pulse fired: elapsed=%v built=%v", elapsedSinceAnchor, fired)
	}
}

// tryBuildAndSubmit builds a milestone via task.Run (which inserts tag-alongs and freezes
// on top of the skeleton) and submits it. Returns true on successful submission.
//
// Target timestamp = max(nowTs, paceMin), where
// paceMin = lastSubmittedTs + TransactionPaceSequencer (ledger-enforced in parse.go).
//
// The pulse cadence (doSequencerSlot) already spaces these attempts ~1 s apart, so nowTs
// is a good enough target — no separate look-ahead offset is needed.
func (seq *Sequencer) tryBuildAndSubmit() bool {
	nowTs := ledger.TimeNow()
	lib := ledger.L(nowTs.Slot)

	// No-branch mode: stop seeking coverage only once the own current-slot milestone has BOTH
	// reached the coverage target (fraction of supply) AND been referenced by another
	// sequencer's milestone — it has done its consolidation job and will be committed with high
	// probability. Until then keep building coverage-seeking (factory) milestones, which also
	// keeps the chain fresh and keeps endorsing peers (raising the odds of being picked up).
	// Evaluated at pulse cadence; consumed by task.Run via SuppressCoverageSeeking.
	if seq.noBranchMode() && !seq.coverageSafe && seq.ownMilestoneHealthyAndReferenced(nowTs.Slot) {
		seq.coverageSafe = true
		seq.Tracef(TraceTagSeqPolicy, "coverage-safe: own milestone reached coverage target and is referenced by a peer in slot %d", nowTs.Slot)
	}

	paceMin := seq.lastSubmittedTs.AddTicks(int(lib.TransactionPaceSequencer))

	targetTs := base.MaximumTime(nowTs, paceMin)

	// don't overshoot into next slot
	nextBoundary := nowTs.NextSlotBoundary()
	if !targetTs.Before(nextBoundary) {
		return false
	}

	// must not be a slot boundary (branches handled separately)
	if targetTs.IsSlotBoundary() {
		return false
	}

	// The branch's chain-predecessor is pace-exempt (ledger scanInputs), so a milestone no
	// longer needs to leave sequencer pace before the boundary branch — the final pre-branch
	// consolidation may land at the last tick. Only the pace against our own previous
	// milestone is enforced here.
	if !ledger.ValidSequencerPace(seq.lastSubmittedTs, targetTs) {
		return false
	}

	seq.newTargetSet()
	seq.slotData.NewTarget()

	msTx, meta, ledgerCoverage, _, err := seq.generateMilestoneForTarget(targetTs)

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
	seq.submitMilestone(msTx, meta, ledgerCoverage, targetTs)
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
	seq.loopMu.Lock()
	if seq.slotData == nil {
		seq.slotData = task.NewSlotData(branchTs.Slot)
	}
	seq.loopMu.Unlock()
	seq.slotData.NewTarget()

	msTx, meta, ledgerCoverage, _, err := seq.generateMilestoneForTarget(branchTs)

	// Branch outcomes don't affect the tag-along budget: branches don't
	// carry tag-along (or delegation) inputs at all (see proposer_base.go).
	// A sequencer that's temporarily unable to propose branches (e.g.
	// coverage out of bounds) should still service tag-aligns through
	// its non-branch milestones — coupling the two starves the
	// tag-along budget unnecessarily.
	switch {
	case errors.Is(err, task.ErrNotGoodEnough):
		seq.slotData.NotGoodEnough()
	case errors.Is(err, task.ErrNoProposals):
		seq.slotData.NoProposals()
	case err != nil:
		seq.Log().Warnf("branch generation: %v (budget: %d/%d)", err, seq.budgetLevel, maxBudgetLevel)
	default:
		util.Assertf(msTx != nil, "msTx != nil")
		meta.TxBytesReceived = util.Ref(time.Now())
		seq.submitMilestone(msTx, meta, ledgerCoverage, branchTs)
	}

	seq.Log().Infof("SLOT STATS: %s, budget: %d/%d", seq.slotData.Lines().Join(", "), seq.budgetLevel, maxBudgetLevel)
	seq.loopMu.Lock()
	seq.slotData = nil
	seq.loopMu.Unlock()

	// advance lastSubmittedTs past the branch boundary even on failure,
	// so the next doSequencerSlot iteration starts at the next slot, not this one
	if branchTs.After(seq.lastSubmittedTs) {
		seq.lastSubmittedTs = branchTs
	}
	return true
}

// submitMilestone sends a milestone to the network fire-and-forget and advances lastSubmittedTs optimistically.
func (seq *Sequencer) submitMilestone(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata, ledgerCoverage uint64, targetTs base.LedgerTime) {
	if !seq.decideSubmitMilestone(tx, ledgerCoverage) {
		seq.lastSubmittedTs = targetTs
		return
	}

	// fire-and-forget: send to input queue and advance optimistically
	seq.OwnSequencerMilestoneIn(tx.Bytes(), meta, tx.ID())
	seq.lastSubmittedTs = tx.Timestamp()
	seq.recordPendingSubmit(tx.ID(), tx.Timestamp())
}

// rollSlotWithoutBranch ends the current slot in no-branch mode: the sequencer issues no
// branch, so it just logs slot stats, clears slotData, and advances lastSubmittedTs past
// the boundary so the next doSequencerSlot iteration starts in the next slot (same outer-loop
// bookkeeping as a branch, minus the branch tx). Returns true to continue, false on MaxTargetTs.
func (seq *Sequencer) rollSlotWithoutBranch(boundaryTs base.LedgerTime) bool {
	if seq.config.MaxTargetTs != base.NilLedgerTime && boundaryTs.After(seq.config.MaxTargetTs) {
		seq.log.Infof("no-branch mode: boundary %s is after maximum ts %s -> stopping", boundaryTs, seq.config.MaxTargetTs)
		return false
	}
	seq.loopMu.Lock()
	sd := seq.slotData
	seq.slotData = nil
	seq.loopMu.Unlock()
	if sd != nil {
		seq.Log().Infof("SLOT STATS (no branch): %s, budget: %d/%d", sd.Lines().Join(", "), seq.budgetLevel, maxBudgetLevel)
	}
	seq.coverageSafe = false
	if boundaryTs.After(seq.lastSubmittedTs) {
		seq.lastSubmittedTs = boundaryTs
	}
	return true
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
