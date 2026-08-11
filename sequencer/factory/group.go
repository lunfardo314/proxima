package factory

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
)

// The skeleton search has no optimal algorithm: reverting own state is inside its space, the
// space is exponential, and it changes underneath the search as candidates arrive and are
// replaced. Everything here is a heuristic, and the mistake to avoid is committing to one. A
// single heuristic that fixes its choice early in the slot can be held on a lineage it should
// have left — an attacker aiming the tag-alongs of conflicting transactions at different
// sequencers forces exactly that, since only one of them can consume a given tag-along and the
// others have to revert to consolidate.
//
// So a group runs several factories concurrently, each searching differently, all feeding one
// output channel and sharing one set of already-checked combinations. Sharing that set is what
// keeps N factories from costing N times the attacher work. The deadline, not the CPU, is the
// binding constraint: each posts a usable skeleton as soon as it has one and improves it while
// time remains.

type (
	// heuristic is how one factory searches: the order it considers endorsement candidates, and
	// which own outputs it offers as the extend. Both draw extends from the whole own past cone,
	// so either can revert.
	heuristic struct {
		name string
		// endorseCandidates returns the peer milestones to endorse, in this heuristic's order.
		endorseCandidates func(f *Factory, targetTs base.LedgerTime) []*vertex.WrappedTx
		// ownExtendCandidates returns own chain outputs to try as the extend, in preference order.
		ownExtendCandidates func(f *Factory) []vertex.WrappedOutput
	}

	// shared is the state one group of factories holds in common.
	shared struct {
		combinations *combinationSet
		// bestCoverage of the skeletons posted for the current target slot. It gates the output
		// channel, so the factories compete there rather than inside their own searches, which
		// keeps each search first-fit and fast.
		bestCoverage atomic.Uint64
		outCh        chan *Skeleton
	}

	// Group is a set of factories searching in parallel for one sequencer.
	Group struct {
		factories []*Factory
		sh        *shared
		cancel    context.CancelFunc
	}
)

// greedyHeuristic exploits: endorse candidates by descending coverage, own outputs newest first.
var greedyHeuristic = heuristic{
	name: "greedy",
	endorseCandidates: func(f *Factory, targetTs base.LedgerTime) []*vertex.WrappedTx {
		return f.Backlog().CandidatesToEndorseSorted(targetTs)
	},
	ownExtendCandidates: func(f *Factory) []vertex.WrappedOutput {
		// newest first: the head carries the work already built into it
		own := f.OwnMilestoneOutputsInMemDAGAscending()
		ret := make([]vertex.WrappedOutput, len(own))
		for i, o := range own {
			ret[len(own)-1-i] = o
		}
		return ret
	},
}

// randomHeuristic explores: both orders shuffled. It breaks the symmetry that has every sequencer
// making the same first-fit choice, and reaches combinations the greedy order never gets to. The
// randomness is in the search only — which skeleton is used is still decided by coverage, so this
// factory cannot produce a worse choice than greedy alone, only additional candidates.
var randomHeuristic = heuristic{
	name: "random",
	endorseCandidates: func(f *Factory, targetTs base.LedgerTime) []*vertex.WrappedTx {
		return f.Backlog().CandidatesToEndorseShuffled(targetTs)
	},
	ownExtendCandidates: func(f *Factory) []vertex.WrappedOutput {
		ret := f.OwnMilestoneOutputsInMemDAGAscending()
		rand.Shuffle(len(ret), func(i, j int) { ret[i], ret[j] = ret[j], ret[i] })
		return ret
	},
}

// NewGroup creates the factories and their shared state. Call Run() to start them.
// The caller reads skeletons from OutCh() and sets the target slot via SetTargetSlot.
func NewGroup(env environment, ctx context.Context) *Group {
	ctx, cancel := context.WithCancel(ctx)
	sh := &shared{
		combinations: newCombinationSet(),
		outCh:        make(chan *Skeleton, 4),
	}
	ret := &Group{sh: sh, cancel: cancel}
	for _, h := range []heuristic{greedyHeuristic, randomHeuristic} {
		ret.factories = append(ret.factories, newFactory(env, ctx, sh, h))
	}
	return ret
}

// Run starts every factory. The group owns the shared output channel and closes it once they have
// all stopped, so a consumer may range over it; an individual factory must not close it.
func (g *Group) Run() {
	var wg sync.WaitGroup
	for _, f := range g.factories {
		wg.Add(1)
		go func(f *Factory) {
			defer wg.Done()
			f.Run()
		}(f)
	}
	go func() {
		wg.Wait()
		close(g.sh.outCh)
	}()
}

// SetTargetSlot advances every factory of the group. Thread-safe.
func (g *Group) SetTargetSlot(slot uint32) {
	for _, f := range g.factories {
		f.SetTargetSlot(slot)
	}
}

func (g *Group) OutCh() <-chan *Skeleton { return g.sh.outCh }

// BestCoverage returns the best skeleton coverage found so far for the current target slot.
func (g *Group) BestCoverage() uint64 { return g.sh.bestCoverage.Load() }

func (g *Group) Stop() { g.cancel() }
