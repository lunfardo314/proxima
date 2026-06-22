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
	"sync/atomic"
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
		FutureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs base.LedgerTime) []vertex.WrappedOutput
		LatestMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
	}

	// Skeleton is a non-branch IncrementalAttacher with extend + endorsements only.
	// Tag-along inputs are the consumer's responsibility.
	Skeleton struct {
		*attacher.IncrementalAttacher
		Coverage uint64 // ledger coverage at the time of creation
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
		bestCoverage        atomic.Uint64
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

// BestCoverage returns the best skeleton coverage found so far for the current target slot.
func (f *Factory) BestCoverage() uint64 {
	return f.bestCoverage.Load()
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
	f.bestCoverage.Store(0)
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
	coverage := skeleton.FinalLedgerCoverage(syntheticTs)
	f.Tracef(TraceTag, "first skeleton: %s, coverage: %d", skeleton.Name(), coverage)

	f.tryPostSkeleton(skeleton, coverage)

	// snapshot own milestone at round start; if it changes, restart from ChooseFirst
	ownMilestoneAtStart := f.GetLatestMilestone(f.SequencerID())

	f.improvementLoop(roundCtx, syntheticTs, skeleton, ownMilestoneAtStart)
}

// improvementLoop tries adding endorsements to improve coverage.
// Uses N persistent workers reading from a job channel.
// Returns when: no untried candidates (stall), own milestone changed, or roundCtx canceled.
func (f *Factory) improvementLoop(roundCtx context.Context, syntheticTs base.LedgerTime, currentBest *attacher.IncrementalAttacher, ownMilestoneAtStart *vertex.WrappedTx) {
	type job struct {
		clone     *attacher.IncrementalAttacher
		candidate *vertex.WrappedTx
	}
	type result struct {
		attacher *attacher.IncrementalAttacher
		coverage uint64
	}

	jobCh := make(chan job, NumImprovementWorkers)
	resultCh := make(chan result, NumImprovementWorkers)

	// start persistent workers
	workerCtx, workerCancel := context.WithCancel(roundCtx)
	defer workerCancel()

	for i := 0; i < NumImprovementWorkers; i++ {
		go func() {
			for j := range jobCh {
				select {
				case <-workerCtx.Done():
					j.clone.Close()
					return
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
				cov := j.clone.FinalLedgerCoverage(syntheticTs)
				resultCh <- result{attacher: j.clone, coverage: cov}
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

		// collect results
		var bestResult *attacher.IncrementalAttacher
		var bestResultCov uint64
		for i := 0; i < sent; i++ {
			r := <-resultCh
			if r.attacher == nil {
				continue
			}
			if r.coverage > bestResultCov {
				if bestResult != nil {
					bestResult.Close()
				}
				bestResult = r.attacher
				bestResultCov = r.coverage
			} else {
				r.attacher.Close()
			}
		}

		if bestResult == nil || !f.tryPostSkeleton(bestResult, bestResultCov) {
			if bestResult != nil {
				bestResult.Close()
			}
			currentBest.Close()
			return
		}

		// improvement found
		currentBest.Close()
		currentBest = bestResult

		f.Tracef(TraceTag, "improved skeleton: %s, coverage: %d, endorsements: %d",
			currentBest.Name(), bestResultCov, len(currentBest.Endorsing()))
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

// tryPostSkeleton posts a skeleton to the output channel if its coverage is at least as good
// as the best so far. Equal coverage is accepted because the outer loop (sequencer) adds
// tag-along and delegation inputs that increase coverage beyond the skeleton's base.
func (f *Factory) tryPostSkeleton(a *attacher.IncrementalAttacher, coverage uint64) bool {
	for {
		current := f.bestCoverage.Load()
		if coverage < current {
			return false
		}
		if f.bestCoverage.CompareAndSwap(current, coverage) {
			break
		}
	}

	clone := a.Clone("skeleton-out")
	sk := &Skeleton{
		IncrementalAttacher: clone,
		Coverage:            coverage,
	}
	select {
	case f.outCh <- sk:
		return true
	case <-f.ctx.Done():
		sk.Close()
		return false
	}
}
