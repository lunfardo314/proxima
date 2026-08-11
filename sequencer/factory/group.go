package factory

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
)

// The skeleton search has no optimal algorithm: reverting own state is inside the search space,
// the space is exponential, and it changes asynchronously as candidates arrive and are replaced.
// Everything here is therefore a heuristic, and the mistake to avoid is committing to one of
// them. A single heuristic that fixes its choice at the start of the slot can be held on a
// lineage it should have left — an attacker aiming the tag-alongs of conflicting transactions at
// different sequencers forces exactly that, since only one of them can consume a given tag-along
// and the others have to revert to consolidate.
//
// So a group runs several factories concurrently, each searching differently, all feeding one
// output channel and sharing one set of already-checked combinations. Sharing that set is what
// keeps the cost of N factories far below N times the work. The deadline, not the CPU, is the
// binding constraint: each factory posts a usable skeleton as soon as it has one and keeps
// improving while time remains.

type (
	// heuristic is how one factory searches: the order it considers endorsement candidates, and
	// which own outputs it offers as the extend. Both factories can revert — extend candidates
	// come from the whole own past cone, not just its head.
	heuristic struct {
		name string
		// endorseCandidates returns the peer milestones to endorse, in the order this heuristic
		// wants them tried.
		endorseCandidates func(f *Factory, targetTs base.LedgerTime) []*vertex.WrappedTx
		// ownExtendCandidates returns own chain outputs to try as the extend, in preference order.
		ownExtendCandidates func(f *Factory) []vertex.WrappedOutput
	}

	// shared is the state one group of factories holds in common.
	shared struct {
		combinations *combinationSet
		// bestScore is the score of the best skeleton posted for the current target slot. It
		// gates the output channel, so the factories compete here rather than inside their own
		// searches — which keeps each search first-fit and fast.
		bestScore atomic.Uint64
		outCh     chan *Skeleton
	}

	// Group is a set of factories searching in parallel for the same sequencer.
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
		return reversed(f.OwnMilestoneOutputsInMemDAGAscending())
	},
}

// randomHeuristic explores: both orders shuffled. It exists to break the symmetry that makes
// every sequencer commit the same first-fit choice, and to reach combinations the greedy order
// would never get to. The randomness is in the search only — which skeleton wins is still
// decided by the score, so adding this factory cannot produce a worse choice than greedy alone.
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

func reversed(s []vertex.WrappedOutput) []vertex.WrappedOutput {
	ret := make([]vertex.WrappedOutput, len(s))
	for i, v := range s {
		ret[len(s)-1-i] = v
	}
	return ret
}

// numSeqShift packs the distinct-sequencer count above the coverage, making the score
// lexicographic: one more sequencer folded into the past cone outweighs any coverage difference.
// That ordering is deliberate. Sibling branches of a slot differ in coverage by around a
// thousandth of a percent — pre-branch consolidation is designed to equalise them so the VRF
// bonus decides the winner — so a rule which moves on a coverage difference alone moves on noise,
// and sequencers chase each other between lineages instead of settling. The sequencer count is an
// integer and carries no such noise. Coverage still breaks ties within an equal count.
//
// Coverage is bounded well below 1<<numSeqShift by the supply, so the two fields do not collide.
const numSeqShift = 52

// score of a skeleton: distinct sequencers in its past cone first, coverage second.
func (f *Factory) score(a *attacher.IncrementalAttacher, targetTs base.LedgerTime) uint64 {
	_, _, numSeq := a.NumNewTransactionStatsInPastCone(f.SequencerID())
	return uint64(numSeq)<<numSeqShift + a.FinalLedgerCoverage(targetTs)
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

// Run starts every factory. The group owns the shared output channel and closes it once they
// have all stopped, so a consumer may range over it; an individual factory must not close it.
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

// BestScore returns the best skeleton score found so far for the current target slot. It is the
// packed score, not a coverage in tokens, and is only ever compared with itself.
func (g *Group) BestScore() uint64 { return g.sh.bestScore.Load() }

func (g *Group) Stop() { g.cancel() }
