// Package factory implements TransactionSkeletonFactory (TSF).
// TSF is a persistent process that continuously scans the tippool and produces
// transaction skeletons (IncrementalAttachers with extend + endorsements, no tag-alongs)
// with strictly increasing coverage.
package factory

import (
	"context"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/backlog"
)

const (
	TraceTag           = "factory"
	NumImprovementWorkers = 3
	TippoolPollInterval   = 50 * time.Millisecond
)

type (
	Environment interface {
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
		Environment
		ctx       context.Context
		cancel    context.CancelFunc
		outCh     chan *Skeleton
		checkedCombinations combinationSet
	}
)

// New creates a new TransactionSkeletonFactory. Call Run() to start it.
// The caller reads skeletons from OutCh().
func New(env Environment, ctx context.Context) *Factory {
	ctx, cancel := context.WithCancel(ctx)
	return &Factory{
		Environment: env,
		ctx:         ctx,
		cancel:      cancel,
		outCh:       make(chan *Skeleton, 4),
	}
}

func (f *Factory) OutCh() <-chan *Skeleton {
	return f.outCh
}

func (f *Factory) Stop() {
	f.cancel()
}

// Run is the main TSF goroutine. It polls the tippool for new own milestones and
// produces skeletons with strictly increasing coverage.
func (f *Factory) Run() {
	defer close(f.outCh)

	var lastOwnMilestoneVID *vertex.WrappedTx

	ticker := time.NewTicker(TippoolPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-f.ctx.Done():
			return
		case <-ticker.C:
		}

		currentOwn := f.GetLatestMilestone(f.SequencerID())
		if currentOwn == nil || currentOwn == lastOwnMilestoneVID {
			continue
		}
		lastOwnMilestoneVID = currentOwn

		f.Tracef(TraceTag, "new own milestone detected: %s", currentOwn.IDShortString())
		f.runRound(currentOwn)
	}
}

// runRound runs one improvement round starting from the given own milestone.
// It finds the first extend-endorse pair, posts it, then tries to improve.
// Returns when improvement is exhausted, context is cancelled, or a new own milestone appears.
func (f *Factory) runRound(ownMilestone *vertex.WrappedTx) {
	f.checkedCombinations = newCombinationSet()

	targetSlot := ledger.TimeNow().Slot
	targetTs := base.T(targetSlot, ledger.L(targetSlot).PostBranchConsolidationTicks)

	skeleton := f.chooseFirstExtendEndorsePair(targetTs, ownMilestone)
	if skeleton == nil {
		return
	}

	coverage := skeleton.FinalLedgerCoverage(targetTs)
	f.Tracef(TraceTag, "first skeleton: %s, coverage: %d", skeleton.Name(), coverage)

	bestCoverage := coverage
	f.postSkeleton(skeleton, coverage)

	// improvement loop with persistent workers
	f.improvementLoop(ownMilestone, targetTs, skeleton, &bestCoverage)
}

// improvementLoop tries adding endorsements to improve coverage.
// Uses N persistent workers reading from a job channel.
func (f *Factory) improvementLoop(ownMilestone *vertex.WrappedTx, targetTs base.LedgerTime, currentBest *attacher.IncrementalAttacher, bestCoverage *uint64) {
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
	workerCtx, workerCancel := context.WithCancel(f.ctx)
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
				cov := j.clone.FinalLedgerCoverage(targetTs)
				resultCh <- result{attacher: j.clone, coverage: cov}
			}
		}()
	}

	defer close(jobCh)

	for {
		// check for new own milestone (round restart)
		if current := f.GetLatestMilestone(f.SequencerID()); current != nil && current != ownMilestone {
			f.Tracef(TraceTag, "new own milestone during improvement, restarting round")
			currentBest.Close()
			return
		}

		select {
		case <-f.ctx.Done():
			currentBest.Close()
			return
		default:
		}

		// get fresh endorsement candidates
		candidates := f.Backlog().CandidatesToEndorseSorted(targetTs)
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
			case <-f.ctx.Done():
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

		if bestResult == nil || bestResultCov <= *bestCoverage {
			if bestResult != nil {
				bestResult.Close()
			}
			currentBest.Close()
			return
		}

		// improvement found
		currentBest.Close()
		currentBest = bestResult
		*bestCoverage = bestResultCov

		f.Tracef(TraceTag, "improved skeleton: %s, coverage: %d, endorsements: %d",
			currentBest.Name(), bestResultCov, len(currentBest.Endorsing()))

		f.postSkeleton(currentBest, bestResultCov)
	}
}

// filterUntried returns endorsement candidates that haven't been checked yet
// with the current skeleton's existing endorsements.
func (f *Factory) filterUntried(currentBest *attacher.IncrementalAttacher, candidates []*vertex.WrappedTx) []*vertex.WrappedTx {
	extend := currentBest.Extending()
	currentEndorsements := currentBest.Endorsing()

	ret := make([]*vertex.WrappedTx, 0, len(candidates))
	for _, c := range candidates {
		// skip if already endorsed
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

// markChecked records that the combination of current skeleton + new candidate has been tried.
func (f *Factory) markChecked(currentBest *attacher.IncrementalAttacher, candidate *vertex.WrappedTx) {
	f.checkedCombinations.markChecked(currentBest.Extending(), currentBest.Endorsing(), candidate)
}

// postSkeleton clones the skeleton and sends the clone to the output channel.
// The caller retains ownership of the original for further improvement.
func (f *Factory) postSkeleton(a *attacher.IncrementalAttacher, coverage uint64) {
	clone := a.Clone("skeleton-out")
	sk := &Skeleton{
		IncrementalAttacher: clone,
		Coverage:            coverage,
	}
	select {
	case f.outCh <- sk:
	case <-f.ctx.Done():
		sk.Close()
	}
}
