// Package factory implements TransactionSkeletonFactory (TSF).
// TSF is a persistent process that continuously scans the tippool and produces
// transaction skeletons (IncrementalAttachers with extend + endorsements, no tag-alongs)
// with strictly increasing score.
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
		AddOwnMilestone(vid *vertex.WrappedTx)
		OwnMilestoneOutputsInMemDAGAscending() []vertex.WrappedOutput
		LatestMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
	}

	// Skeleton is a non-branch IncrementalAttacher with extend + endorsements only.
	// Tag-along inputs are the consumer's responsibility.
	Skeleton struct {
		*attacher.IncrementalAttacher
		// Score at the time of creation: distinct sequencers folded into the past cone first,
		// coverage second. Comparable only with other skeleton scores.
		Score uint64
	}

	Factory struct {
		environment
		ctx    context.Context
		cancel context.CancelFunc

		sh *shared
		h  heuristic

		slotMutex   sync.RWMutex
		targetSlot  uint32 // 0 means not set
		roundCancel context.CancelFunc
	}
)

// newFactory creates one factory of a group, searching with the given heuristic and sharing
// state with its siblings. Call Run() to start it.
func newFactory(env environment, ctx context.Context, sh *shared, h heuristic) *Factory {
	ctx, cancel := context.WithCancel(ctx)
	return &Factory{
		environment: env,
		ctx:         ctx,
		cancel:      cancel,
		sh:          sh,
		h:           h,
	}
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
	f.sh.bestScore.Store(0)
	// the shared combination set is reset by each factory's Run goroutine on its slot change,
	// not here: clearing it from the caller would race the rounds still finishing on the old slot

	f.Tracef(TraceTag, "SetTargetSlot: %d", slot)
}

func (f *Factory) getTargetSlot() uint32 {
	f.slotMutex.RLock()
	defer f.slotMutex.RUnlock()
	return f.targetSlot
}

// Run is the main TSF goroutine. It waits for a target slot to be set,
// then continuously tries to produce skeletons with increasing score.
func (f *Factory) Run() {

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
			// Reset the shared per-slot dedup. Every factory of the group does this on its own
			// slot change; whichever gets there first clears it and the others find it already
			// empty for that slot, which is harmless — the set only ever suppresses work.
			f.sh.combinations.reset()
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

// runRound runs one improvement round for the given slot.
// Returns when improvement is exhausted, a new own milestone appears, or roundCtx is canceled.
func (f *Factory) runRound(roundCtx context.Context, slot uint32) {
	skeleton := f.chooseFirstExtendEndorsePair(slot)
	if skeleton == nil {
		return
	}

	syntheticTs := base.T(slot, base.MaxTickValue)
	sc := f.score(skeleton, syntheticTs)
	f.Tracef(TraceTag, "[%s] first skeleton: %s, score: %d", f.h.name, skeleton.Name(), sc)

	f.tryPostSkeleton(skeleton, sc)

	// snapshot own milestone at round start; if it changes, restart from ChooseFirst
	ownMilestoneAtStart := f.GetLatestMilestone(f.SequencerID())

	f.improvementLoop(roundCtx, syntheticTs, skeleton, ownMilestoneAtStart)
}

// improvementLoop tries adding endorsements to improve the score.
// Uses N persistent workers reading from a job channel.
// Returns when: no untried candidates (stall), own milestone changed, or roundCtx canceled.
func (f *Factory) improvementLoop(roundCtx context.Context, syntheticTs base.LedgerTime, currentBest *attacher.IncrementalAttacher, ownMilestoneAtStart *vertex.WrappedTx) {
	type job struct {
		clone     *attacher.IncrementalAttacher
		candidate *vertex.WrappedTx
	}
	type result struct {
		attacher *attacher.IncrementalAttacher
		score    uint64
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
				resultCh <- result{attacher: j.clone, score: f.score(j.clone, syntheticTs)}
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
		var bestResultScore uint64
		for i := 0; i < sent; i++ {
			r := <-resultCh
			if r.attacher == nil {
				continue
			}
			if r.score > bestResultScore {
				if bestResult != nil {
					bestResult.Close()
				}
				bestResult = r.attacher
				bestResultScore = r.score
			} else {
				r.attacher.Close()
			}
		}

		if bestResult == nil || !f.tryPostSkeleton(bestResult, bestResultScore) {
			if bestResult != nil {
				bestResult.Close()
			}
			currentBest.Close()
			return
		}

		// improvement found
		currentBest.Close()
		currentBest = bestResult

		f.Tracef(TraceTag, "improved skeleton: %s, score: %d, endorsements: %d",
			currentBest.Name(), bestResultScore, len(currentBest.Endorsing()))
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
		if f.sh.combinations.isChecked(extend, currentEndorsements, c) {
			continue
		}
		ret = append(ret, c)
	}
	return ret
}

func (f *Factory) markChecked(currentBest *attacher.IncrementalAttacher, candidate *vertex.WrappedTx) {
	f.sh.combinations.markChecked(currentBest.Extending(), currentBest.Endorsing(), candidate)
}

// tryPostSkeleton posts a skeleton to the output channel if its score is at least as good
// as the best so far. An equal score is accepted because the outer loop (sequencer) adds
// tag-along and delegation inputs that raise coverage beyond the skeleton's base.
func (f *Factory) tryPostSkeleton(a *attacher.IncrementalAttacher, score uint64) bool {
	for {
		current := f.sh.bestScore.Load()
		if score < current {
			return false
		}
		if f.sh.bestScore.CompareAndSwap(current, score) {
			break
		}
	}

	clone := a.Clone("skeleton-out")
	sk := &Skeleton{
		IncrementalAttacher: clone,
		Score:               score,
	}
	select {
	case f.sh.outCh <- sk:
		return true
	case <-f.ctx.Done():
		sk.Close()
		return false
	}
}
