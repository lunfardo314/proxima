package v3

import (
	"errors"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/v3/task"
	"github.com/lunfardo314/proxima/util"
)

const (
	// milestoneWatchInterval is how often the background watcher polls the tippool.
	milestoneWatchInterval = 20 * time.Millisecond

	// lateBranchToleranceTicks is how far past its boundary a latched branch may still be
	// submitted. It absorbs scheduling jitter in the loop; beyond it the branch is stale enough
	// that the slot is better conceded than contested.
	lateBranchToleranceTicks = 4

	// finalConsolidationLeadTicks is how many ticks before the slot boundary the final pre-branch
	// milestone is stamped. Early enough to be built (the factory gets the peers' closing
	// milestones, which arrive up to ~600 ms after their tick) and attached before the branch; late
	// enough that everything issued before the zone has arrived. With the zone opening 25 ticks
	// before the boundary it leaves the closing milestone room under a sequencer pace of 6.
	finalConsolidationLeadTicks = 7

	// finalConsolidationLastTryTicks: the final milestone is retried every tick until this many
	// ticks before the boundary, leaving the last ones to the build, the attachment and the branch.
	finalConsolidationLastTryTicks = 2

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
//   - in the pre-branch consolidation zone regular pulses are held; two endorse-only milestones
//     (1 input, the ledger PreBranchConsolidationTicks rule) close the slot, the closing one early
//     in the zone and the final one finalConsolidationLeadTicks before the boundary;
//   - the branch is submitted at the last tick of the slot, extending the freshest own milestone.
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
	// Pulse interval = cfg.Pace ticks of wall-clock, see kb/archive/shipped/seq-improvements.md.
	// Default cfg.Pace = defaultSequencerPaceTicks (12); tests override via WithPace().
	pulseInterval := time.Duration(seq.config.Pace) * tickDuration

	ticker := time.NewTicker(tickDuration)
	defer ticker.Stop()

	// Per-slot guards: the closing milestone is tried at most once per slot, the final one until
	// issued or out of time. zoneSlot tracks the slot they apply to so they reset when the loop
	// rolls into a new slot.
	closingTried := false
	finalConsolidationDone := false
	zoneSlot := uint32(0)
	// whether the slot's first regular pulse has fired: that one is not held by the pulse interval
	firstPulseFired := false
	var heldSlot uint32 // the slot whose first move has been counted as held

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
			firstPulseFired = false
			closingTried = false
			finalConsolidationDone = false
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
		// So two milestones close the slot (kb/sequencer_search.md). The closing milestone, at the first tick of the zone
		// the pace allows, endorses the peers' latest milestones early enough for every peer to
		// see it. The final one, finalConsolidationLeadTicks before the boundary, endorses the
		// peers' closing milestones; it is retried each tick while it finds nothing, since the
		// closings arrive spread over several ticks. A zone milestone consumes nothing but its
		// chain predecessor, so a final that has folded in every peer's closing already holds the
		// slot's maximum; nothing a later milestone could add. Built at the last tick instead,
		// with one tick to build, it was skipped in most slots. The branch, exempt from the pace
		// (ledger scanInputs), goes out at the last tick on the freshest own milestone. Either
		// milestone is skipped when it has nothing new to raise coverage with, which the ledger
		// requires. Regular pulses are held through the zone.
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
			finalConsolidationTs := nextBoundary.AddTicks(-finalConsolidationLeadTicks)
			seq.pendingSubmitMu.Lock()
			awaiting := seq.pendingSubmit.awaiting
			seq.pendingSubmitMu.Unlock()
			// before the final-consolidation tick only the closing milestone is issued, once, as
			// soon as the previous own milestone has attached and the pace allows
			if nowTs.Before(finalConsolidationTs) {
				if !closingTried && !awaiting && ledger.ValidSequencerPace(seq.lastSubmittedTs, nowTs) {
					closingTried = true
					submitted := seq.tryBuildAndSubmit()
					seq.slotData.ZoneMilestone("closing", submitted)
					if submitted {
						seq.loopMu.Lock()
						seq.lastPulseAnchor = time.Now()
						seq.loopMu.Unlock()
					}
				}
				continue
			}
			// the final consolidation: tried every tick, once the previous own milestone has
			// attached, until it is issued or the last ticks are reached (tryBuildAndSubmit stamps
			// it no earlier than the pace allows)
			if !finalConsolidationDone {
				if ticksToSlotEnd > finalConsolidationLastTryTicks {
					if !awaiting && seq.tryBuildAndSubmit() {
						finalConsolidationDone = true
						seq.slotData.ZoneMilestone("final", true)
						seq.loopMu.Lock()
						seq.lastPulseAnchor = time.Now()
						seq.loopMu.Unlock()
					}
					continue
				}
				finalConsolidationDone = true
				seq.slotData.ZoneMilestone("final", false)
			}
			// the branch at the last tick, so the final consolidation has attached and the branch
			// extends it; if it has not, the branch extends the latest attached own milestone
			if ticksToSlotEnd > 1 {
				continue
			}
			seq.Tracef(TraceTagSeqPolicy, "branch time: submitting branch at %s", nextBoundary)
			return seq.generateAndSubmitBranch(nextBoundary)
		}

		// No milestone is built before the post-branch consolidation tick, so no pulse fires there.
		// A pulse failing only because it is early would still restart the pulse interval, pushing
		// the slot's first milestone up to a whole interval past that tick, when the others' own
		// branches are no longer bare.
		if nowTs.Tick < ledger.L(currentSlot).PostBranchConsolidationTicks {
			continue
		}
		if seq.holdFirstMove(nowTs) {
			if heldSlot != currentSlot {
				heldSlot = currentSlot
				seq.slotData.Count("firstMoveHeld")
			}
			continue
		}

		// --- Pulse gate (primary): previous own milestone not yet observed in tippool. ---
		seq.pendingSubmitMu.Lock()
		awaiting := seq.pendingSubmit.awaiting
		seq.pendingSubmitMu.Unlock()
		if awaiting {
			continue
		}

		// --- Pulse gate (timing): pulseInterval must have elapsed since anchor, except for the
		// slot's first pulse, which goes out as soon as the slot allows a milestone. ---
		seq.loopMu.Lock()
		elapsedSinceAnchor := time.Since(seq.lastPulseAnchor)
		seq.loopMu.Unlock()
		if firstPulseFired && elapsedSinceAnchor < pulseInterval && !seq.settlementPulseDue(nowTs, lib.PreBranchConsolidationTicks) {
			continue
		}

		// Pulse fires: attempt build and submit. Advance the anchor regardless of outcome
		// so we don't rapid-fire after a failed attempt. On success, the tippool
		// observation of the new milestone will further advance the anchor.
		firstPulseFired = true
		fired := seq.tryBuildAndSubmit()
		seq.loopMu.Lock()
		seq.lastPulseAnchor = time.Now()
		seq.loopMu.Unlock()
		seq.Tracef(TraceTagSeqPolicy, "pulse fired: elapsed=%v built=%v", elapsedSinceAnchor, fired)
	}
}

// firstMoveGraceTicks is how long past the post-branch consolidation tick the first move waits for a
// pair on another sequencer's branch. The slot's branches attach right around that tick, and a pair on
// one is ready later still (its baseline is committed lazily), so a first move built at the tick often
// has only the own branch to extend, stays on it, and stalls the sequencer when another branch is picked.
const firstMoveGraceTicks = 6

// holdFirstMove tells whether the slot's first move waits: the own branch of the slot is the own latest
// milestone and the factory has not yet posted a skeleton endorsing another sequencer's milestone. A
// foreign milestone merely present in the tippool is not enough: the pairs on it may not be buildable yet.
func (seq *Sequencer) holdFirstMove(nowTs base.LedgerTime) bool {
	if seq.skeletonFactory == nil {
		return false
	}
	own := seq.GetLatestMilestone(seq.sequencerID)
	ownBranchOnly := own != nil && own.IsBranchTransaction() && own.Slot() == nowTs.Slot
	return firstMoveHeld(nowTs.Tick, ledger.L(nowTs.Slot).PostBranchConsolidationTicks, ownBranchOnly, seq.skeletonFactory.EndorsingPosted())
}

// firstMoveHeld is the decision of holdFirstMove: hold while the own branch is the only pair to build
// from, until the grace past the post-branch consolidation tick ends. Past it the first move goes out on
// the own branch, as a sequencer alone or far ahead must.
func firstMoveHeld(tick, postBranchTicks uint8, ownBranchOnly, endorsingPosted bool) bool {
	return ownBranchOnly && !endorsingPosted && tick < postBranchTicks+firstMoveGraceTicks
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

	// No milestone before the post-branch consolidation tick of the slot (0 before library
	// upgrade 2). Waiting for the wall clock, rather than building early with a later timestamp,
	// is the point: the first milestone of the slot is then built with every branch that arrived
	// in the window in view.
	targetLib := ledger.L(targetTs.Slot)
	if targetTs.Tick < targetLib.PostBranchConsolidationTicks {
		return false
	}
	// below the minimum own balance the ledger refuses a milestone in the pre-branch zone
	if targetLib.IsPreBranchConsolidationTimestamp(targetTs) && seq.belowMinBalance(targetTs.Slot) {
		return false
	}
	// In the pre-branch consolidation zone, from upgrade 2 on, the chain predecessor must be of
	// the same slot: a sequencer that issued nothing earlier in the slot builds nothing here.
	if targetLib.RequiresSameSlotChainPredecessor(targetTs) {
		if own := seq.OwnLatestMilestoneOutput(); own.VID == nil || own.VID.Slot() != targetTs.Slot {
			return false
		}
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

	// The build blocks the loop, so a build running past the slot boundary would make the branch
	// late enough to be dropped. No milestone of the slot is worth its branch: the build is bounded
	// by the boundary, and a milestone finished after it is not submitted.
	buildDeadline := ledger.ClockTime(nextBoundary)
	msTx, meta, ledgerCoverage, _, err := seq.generateMilestoneForTarget(targetTs, buildDeadline)

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
	if time.Now().After(buildDeadline) {
		seq.slotData.LateDropped()
		seq.Tracef(TraceTagSeqPolicy, "milestone %s built after the slot boundary, not submitted", msTx.IDShortString)
		return false
	}

	own := seq.GetLatestMilestone(seq.sequencerID)
	firstMove := own == nil || own.Slot() < targetTs.Slot || own.IsBranchTransaction()
	meta.TxBytesReceived = util.Ref(time.Now())
	seq.submitMilestone(msTx, meta, ledgerCoverage, targetTs)
	seq.adjustBudget(true)
	if firstMove {
		// off the loop: the classification may read a branch state still pending commit
		slotData := seq.slotData
		go func() { slotData.Count(seq.classifyFirstMove(msTx)) }()
	}
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

	msTx, meta, ledgerCoverage, _, err := seq.generateMilestoneForTarget(branchTs, time.Time{})

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
		// recorded here: the tippool echo of the branch arrives after the stats are printed
		seq.slotData.BranchTxSubmitted(msTx.ID())
	}

	factoryCounters := "none"
	if seq.skeletonFactory != nil {
		factoryCounters = seq.skeletonFactory.CountersString()
	}
	seq.Log().Infof("SLOT STATS: %s, factory: %s, budget: %d/%d", seq.slotData.Lines().Join(", "), factoryCounters, seq.budgetLevel, maxBudgetLevel)
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
