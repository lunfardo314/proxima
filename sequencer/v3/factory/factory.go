// Package factory implements TransactionSkeletonFactory (TSF).
// TSF is a persistent process that continuously scans the tippool and produces
// transaction skeletons (IncrementalAttachers with extend + endorsements, no tag-alongs)
// with strictly increasing coverage.
// The factory operates within a target slot set externally via SetTargetSlot.
// It does not use wall clock — only ledger time (logical clock).
package factory

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/backlog"
)

const (
	TraceTag              = "factory"
	NumImprovementWorkers = 3
	RunLoopPollInterval   = 50 * time.Millisecond
)

type (
	environment interface {
		global.NodeGlobal
		attacher.Environment
		SequencerID() base.ChainID
		SequencerName() string
		GetLatestMilestone(seqID base.ChainID) *vertex.WrappedTx
		Backlog() *backlog.TagAlongBacklog
		RankByScore
		AddOwnMilestone(vid *vertex.WrappedTx)
		OwnMilestoneOutputsInMemDAGAscending() []vertex.WrappedOutput
		LatestMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
	}

	// Skeleton is a non-branch IncrementalAttacher with extend + endorsements only.
	// Tag-along inputs are the consumer's responsibility.
	Skeleton struct {
		*attacher.IncrementalAttacher
		Value uint64 // RankValue at the time of creation
	}

	Factory struct {
		environment
		ctx    context.Context
		cancel context.CancelFunc
		outCh  chan *Skeleton

		slotMutex           sync.RWMutex
		targetSlot          uint32 // 0 means not set
		roundCancel         context.CancelFunc
		checkedCombinations combinationSet
		// own chain output committed in each branch, read once per target slot
		committedOwn map[base.TransactionID]vertex.WrappedOutput
		// RankValue of the best skeleton posted in the target slot, and whether one of them endorses
		// another sequencer's milestone. Guarded by slotMutex together with targetSlot: a round of the
		// previous slot, still finishing when the slot changes, must not set them for the new slot.
		bestValue       uint64
		endorsingPosted bool
		// per-slot diagnostics for SLOT STATS, reset when a new slot is targeted
		counters     Counters
		countersSlot uint32
	}
)

// New creates a new TransactionSkeletonFactory. Call Run() to start it.
// The caller reads skeletons from OutCh() and sets the target slot via SetTargetSlot.
func New(env environment, ctx context.Context) *Factory {
	ctx, cancel := context.WithCancel(ctx)
	return &Factory{
		environment: env,
		ctx:         ctx,
		cancel:      cancel,
		outCh:       make(chan *Skeleton, 4),
	}
}

func (f *Factory) OutCh() <-chan *Skeleton {
	return f.outCh
}

// EndorsingPosted tells whether a skeleton endorsing another sequencer's milestone has been posted in
// the target slot: the first move then has a pair other than the own branch alone to be built from.
func (f *Factory) EndorsingPosted() bool {
	f.slotMutex.RLock()
	defer f.slotMutex.RUnlock()
	return f.endorsingPosted
}

// CountersString lists the diagnostic counters of the current slot.
func (f *Factory) CountersString() string {
	return f.counters.String()
}

// BestValue returns the RankValue of the best skeleton found so far for the current target slot.
func (f *Factory) BestValue() uint64 {
	f.slotMutex.RLock()
	defer f.slotMutex.RUnlock()
	return f.bestValue
}

func (f *Factory) Stop() {
	f.cancel()
}

// SetTargetSlot sets the target slot and restarts any ongoing round.
// Thread-safe — can be called from any goroutine.
func (f *Factory) SetTargetSlot(slot uint32) {
	f.slotMutex.Lock()
	defer f.slotMutex.Unlock()

	if f.targetSlot == slot {
		return
	}

	// cancel the current round if running
	if f.roundCancel != nil {
		f.roundCancel()
		f.roundCancel = nil
	}

	f.targetSlot = slot
	f.bestValue = 0
	f.endorsingPosted = false
	// slot 0 only pauses the factory within a slot (no-branch mode): keep that slot's counts
	if slot != 0 && slot != f.countersSlot {
		f.countersSlot = slot
		f.counters.Reset()
	}
	// checkedCombinations is owned by the Run goroutine and reset there on slot change;
	// resetting it here would race with isChecked/markChecked in the running round.

	f.Tracef(TraceTag, "SetTargetSlot: %d", slot)
}

func (f *Factory) getTargetSlot() uint32 {
	f.slotMutex.RLock()
	defer f.slotMutex.RUnlock()
	return f.targetSlot
}

// Run is the main TSF goroutine. It waits for a target slot to be set,
// then continuously tries to produce skeletons with increasing coverage.
func (f *Factory) Run() {
	defer close(f.outCh)

	ticker := time.NewTicker(RunLoopPollInterval)
	defer ticker.Stop()

	var lastSlot uint32

	for {
		select {
		case <-f.ctx.Done():
			return
		case <-ticker.C:
		}

		slot := f.getTargetSlot()
		if slot == 0 {
			continue
		}

		if slot != lastSlot {
			lastSlot = slot
			// reset per-slot dedup here, on the Run goroutine that owns it
			f.checkedCombinations = newCombinationSet()
			f.committedOwn = make(map[base.TransactionID]vertex.WrappedOutput)
			f.Tracef(TraceTag, "starting round for slot %d", slot)
		}

		// create a round-scoped context that SetTargetSlot can cancel
		f.slotMutex.Lock()
		roundCtx, roundCancel := context.WithCancel(f.ctx)
		f.roundCancel = roundCancel
		f.slotMutex.Unlock()

		f.runRound(roundCtx, slot)
		roundCancel()
	}
}

// runRound runs one round for the given slot: the search for the best first move while the
// sequencer has no milestone of the slot yet, an improvement round otherwise.
// Returns when the round is exhausted, a new own milestone appears, or roundCtx is canceled.
func (f *Factory) runRound(roundCtx context.Context, slot uint32) {
	if f.firstMovePending(slot) {
		f.searchFirstMove(roundCtx, slot)
		return
	}
	seeds := f.seedPairs(slot)
	// One loop per lineage only in a slot ranked by score; before, as v2: the better seed alone,
	// improved while it beats the best posted in the slot.
	perLineage := f.RankByScore(slot)
	if !perLineage && len(seeds) > 1 {
		for _, rest := range seeds[1:] {
			rest.Close()
		}
		seeds = seeds[:1]
	}
	syntheticTs := base.T(slot, base.MaxTickValue)
	if len(seeds) == 0 {
		f.counters.Inc("seedNone")
	}

	// one improvement loop per lineage, the highest ranked first
	for i, skeleton := range seeds {
		if roundCtx.Err() != nil {
			for _, rest := range seeds[i:] {
				rest.Close()
			}
			return
		}
		value := RankValue(f, skeleton, syntheticTs)
		if skeleton.Extending().VID.Slot() < slot {
			f.counters.Inc("seedReanchor")
		} else {
			f.counters.Inc("seedHead")
		}
		f.Tracef(TraceTag, "seed skeleton: %s, value: %d", skeleton.Name(), value)
		f.tryPostSkeleton(roundCtx, slot, skeleton, value, false)
		// snapshot own milestone after the post, which may block until a milestone is built; if it
		// changes, the loop ends and the round restarts from the seeds
		ownMilestoneAtStart := f.GetLatestMilestone(f.SequencerID())
		f.improvementLoop(roundCtx, syntheticTs, skeleton, value, perLineage, ownMilestoneAtStart)
	}
}

// improvementLoop tries adding endorsements to improve one lineage's skeleton (kb/coverage_adjustment.md). With localThreshold
// it goes on while a step improves on the previous one and posts the steps at least as good as the
// best posted in the slot, so a lineage that starts below another can still overtake it; without,
// it stops at the first step that is not posted.
// Uses N persistent workers reading from a job channel.
// Returns when: no step improves (stall), own milestone changed, or roundCtx canceled.
func (f *Factory) improvementLoop(roundCtx context.Context, syntheticTs base.LedgerTime, currentBest *attacher.IncrementalAttacher, currentValue uint64, localThreshold bool, ownMilestoneAtStart *vertex.WrappedTx) {
	type job struct {
		clone     *attacher.IncrementalAttacher
		candidate *vertex.WrappedTx
	}
	type result struct {
		attacher *attacher.IncrementalAttacher
		value    uint64
	}

	jobCh := make(chan job, NumImprovementWorkers)
	resultCh := make(chan result, NumImprovementWorkers)

	// start persistent workers
	workerCtx, workerCancel := context.WithCancel(roundCtx)
	defer workerCancel()

	for i := 0; i < NumImprovementWorkers; i++ {
		go func() {
			// Invariant: exactly one result per job taken off jobCh. The collector below waits
			// for precisely as many results as it sent jobs, so a worker that consumes a job and
			// returns without answering wedges the collector — and with it runRound, the Run loop
			// and the whole factory — for the life of the process. That includes cancellation:
			// answer, then keep draining, and let the range end when jobCh is closed.
			for j := range jobCh {
				select {
				case <-workerCtx.Done():
					j.clone.Close()
					resultCh <- result{}
					continue
				default:
				}
				if err := j.clone.InsertEndorsement(j.candidate); err != nil {
					j.clone.Close()
					resultCh <- result{}
					continue
				}
				if !j.clone.Completed() {
					j.clone.Close()
					resultCh <- result{}
					continue
				}
				resultCh <- result{attacher: j.clone, value: RankValue(f, j.clone, syntheticTs)}
			}
		}()
	}

	defer close(jobCh)

	for {
		select {
		case <-roundCtx.Done():
			currentBest.Close()
			return
		default:
		}

		// check if own milestone changed — restart from ChooseFirst to pick up new extend candidates
		if current := f.GetLatestMilestone(f.SequencerID()); current != ownMilestoneAtStart {
			f.Tracef(TraceTag, "own milestone changed, restarting round")
			currentBest.Close()
			return
		}

		// get fresh endorsement candidates
		candidates := f.Backlog().CandidatesToEndorseSorted(syntheticTs)
		untried := f.filterUntried(currentBest, candidates)
		if len(untried) == 0 {
			currentBest.Close()
			return
		}

		// send jobs
		sent := 0
		for _, candidate := range untried {
			clone := currentBest.Clone("improve-" + candidate.IDShortString())
			f.markChecked(currentBest, candidate)

			select {
			case jobCh <- job{clone: clone, candidate: candidate}:
				sent++
			case <-roundCtx.Done():
				clone.Close()
				currentBest.Close()
				return
			}
			if sent >= NumImprovementWorkers {
				break
			}
		}

		// Collect results: exactly one per job sent (the workers guarantee it, cancellation
		// included). Receiving a fixed count is what keeps in-flight attachers from leaking,
		// so this must not bail out early — it relies on that invariant instead.
		var bestResult *attacher.IncrementalAttacher
		var bestResultValue uint64
		for i := 0; i < sent; i++ {
			r := <-resultCh
			if r.attacher == nil {
				continue
			}
			if r.value > bestResultValue {
				if bestResult != nil {
					bestResult.Close()
				}
				bestResult = r.attacher
				bestResultValue = r.value
			} else {
				r.attacher.Close()
			}
		}

		posted := bestResult != nil && f.tryPostSkeleton(roundCtx, syntheticTs.Slot, bestResult, bestResultValue, false)
		improves := bestResult != nil && bestResultValue > currentValue
		if !posted && !(localThreshold && improves) {
			if bestResult != nil {
				bestResult.Close()
			}
			currentBest.Close()
			return
		}

		// improvement found
		currentBest.Close()
		currentBest = bestResult
		currentValue = bestResultValue

		f.Tracef(TraceTag, "improved skeleton: %s, value: %d, endorsements: %d",
			currentBest.Name(), bestResultValue, len(currentBest.Endorsing()))
	}
}

// filterUntried returns endorsement candidates that haven't been checked yet
// with the current skeleton's existing endorsements.
func (f *Factory) filterUntried(currentBest *attacher.IncrementalAttacher, candidates []*vertex.WrappedTx) []*vertex.WrappedTx {
	extend := currentBest.Extending()
	currentEndorsements := currentBest.Endorsing()

	ret := make([]*vertex.WrappedTx, 0, len(candidates))
	for _, c := range candidates {
		alreadyEndorsed := false
		for _, e := range currentEndorsements {
			if e == c {
				alreadyEndorsed = true
				break
			}
		}
		if alreadyEndorsed {
			continue
		}
		if f.checkedCombinations.isChecked(extend, currentEndorsements, c) {
			continue
		}
		ret = append(ret, c)
	}
	return ret
}

func (f *Factory) markChecked(currentBest *attacher.IncrementalAttacher, candidate *vertex.WrappedTx) {
	f.checkedCombinations.markChecked(currentBest.Extending(), currentBest.Endorsing(), candidate)
}

// tryPostSkeleton posts a skeleton to the output channel if its RankValue is at least as good
// as the best so far. An equal value is accepted because the outer loop (sequencer) adds
// tag-along and delegation inputs that increase it beyond the skeleton's base.
//
// The channel is drained only when a milestone is built. A blocking post pauses the factory
// until then, which keeps the improvement rounds from repeating their work in between. It ends
// with the round, so a change of the target slot releases it: nothing is built before the first
// milestone tick of the slot, and a post left blocked from the previous slot would hold the
// first-move search until that tick. The first-move search posts with dropOldest instead, for the
// same reason. A full channel then drops its oldest skeleton: skeletons are posted with
// non-decreasing coverage within a slot, and one of an earlier slot is discarded by the consumer
// anyway.
//
// slot is the slot the round works for. A round still finishing after the target slot changed posts
// nothing: its value would become the new slot's threshold, and a skeleton from the end of a slot
// outranks every skeleton early in the next one, so the factory would post nothing for that slot.
func (f *Factory) tryPostSkeleton(roundCtx context.Context, slot uint32, a *attacher.IncrementalAttacher, value uint64, dropOldest bool) bool {
	if !f.raiseBestValue(slot, value) {
		return false
	}

	clone := a.Clone("skeleton-out")
	sk := &Skeleton{
		IncrementalAttacher: clone,
		Value:               value,
	}
	// read before sending: once in the channel the skeleton belongs to the proposer
	endorsing := len(clone.Endorsing()) > 0
	for {
		if !dropOldest {
			select {
			case f.outCh <- sk:
				f.posted(slot, endorsing)
				return true
			case <-roundCtx.Done():
				sk.Close()
				return false
			}
		}
		select {
		case f.outCh <- sk:
			f.posted(slot, endorsing)
			return true
		case <-roundCtx.Done():
			sk.Close()
			return false
		default:
		}
		select {
		case old, ok := <-f.outCh:
			if ok {
				old.Close()
			}
		default:
		}
	}
}

// raiseBestValue raises the best value of the target slot to value, if slot is still the target and
// value is not below it.
func (f *Factory) raiseBestValue(slot uint32, value uint64) bool {
	f.slotMutex.Lock()
	defer f.slotMutex.Unlock()
	switch {
	case slot != f.targetSlot:
		f.counters.Inc("stalePost")
		return false
	case value < f.bestValue:
		f.counters.Inc("belowBest")
		return false
	}
	f.bestValue = value
	return true
}

func (f *Factory) posted(slot uint32, endorsing bool) {
	f.counters.Inc("posted")
	if !endorsing {
		return
	}
	f.slotMutex.Lock()
	defer f.slotMutex.Unlock()
	if slot == f.targetSlot {
		f.endorsingPosted = true
	}
}
