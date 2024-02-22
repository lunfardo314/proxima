package backlog

import (
	"slices"
	"sort"
	"sync"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	Environment interface {
		global.Logging
		ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput))
		PullSequencerTips(seqID ledger.ChainID, loadOwnMilestones bool) (set.Set[vertex.WrappedOutput], error)
		SequencerID() ledger.ChainID
		SequencerName() string
		GetLatestMilestone(seqID ledger.ChainID) *vertex.WrappedTx
		LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		NumSequencerTips() int
	}

	Backlog struct {
		Environment
		mutex                    sync.RWMutex
		outputs                  set.Set[vertex.WrappedOutput]
		outputCount              int
		removedOutputsSinceReset int
	}

	Stats struct {
		NumOtherSequencers       int
		NumOutputs               int
		OutputCount              int
		RemovedOutputsSinceReset int
	}
)

// TODO tag-along and delegation locks

type Option byte

// OptionDoNotLoadOwnMilestones is used for tests only
const (
	TraceTag                     = "backlog"
	OptionDoNotLoadOwnMilestones = Option(iota)
)

func New(env Environment, opts ...Option) (*Backlog, error) {
	seqID := env.SequencerID()
	ret := &Backlog{
		Environment: env,
		outputs:     set.New[vertex.WrappedOutput](),
	}
	env.Tracef(TraceTag, "starting tipPool..")

	ret.mutex.RLock()
	defer ret.mutex.RUnlock()

	// start listening to chain-locked account
	env.ListenToAccount(seqID.AsChainLock(), func(wOut vertex.WrappedOutput) {
		env.Tracef(TraceTag, "[%s] output IN: %s", ret.SequencerName, wOut.IDShortString)

		if !isCandidateToTagAlong(wOut) {
			return
		}

		{
			// sanity check
			wOut.VID.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
				util.Assertf(int(wOut.Index) < v.Tx.NumProducedOutputs(), "int(wOut.Index) < v.Tx.NumProducedOutputs()")
			}})
		}

		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		if !ret.outputs.InsertNew(wOut) {
			env.Tracef(TraceTag, "repeating output %s", wOut.IDShortString)
			return
		}
		ret.outputCount++
		env.Tracef(TraceTag, "output stored in tippool: %s (total: %d)", wOut.IDShortString, len(ret.outputs))
	})

	// fetch all sequencers and all outputs in the sequencer account into to tip pool once
	var err error
	doNotLoadOwnMilestones := slices.Index(opts, OptionDoNotLoadOwnMilestones) >= 0
	env.Tracef(TraceTag, "PullSequencerTips: doNotLoadOwnMilestones = %v", doNotLoadOwnMilestones)
	ret.outputs, err = env.PullSequencerTips(seqID, !doNotLoadOwnMilestones)
	if err != nil {
		return nil, err
	}
	env.Tracef(TraceTag, "PullSequencerTips: loaded %d outputs from state", len(ret.outputs))
	return ret, nil
}

func (tp *Backlog) CandidatesToEndorseSorted(targetTs ledger.Time) []*vertex.WrappedTx {
	targetSlot := targetTs.Slot()
	ownSeqID := tp.SequencerID()
	return tp.LatestMilestonesDescending(func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool {
		return vid.Slot() == targetSlot && seqID != ownSeqID
	})
}

func (tp *Backlog) GetOwnLatestMilestoneTx() *vertex.WrappedTx {
	return tp.GetLatestMilestone(tp.SequencerID())
}

func isCandidateToTagAlong(wOut vertex.WrappedOutput) bool {
	if wOut.VID.IsBadOrDeleted() {
		return false
	}
	if wOut.VID.IsBranchTransaction() {
		// outputs of branch transactions are filtered out
		// TODO probably ordinary outputs must not be allowed at ledger level
		return false
	}
	o, err := wOut.VID.OutputAt(wOut.Index)
	if err != nil {
		return false
	}
	if o != nil {
		if _, idx := o.ChainConstraint(); idx != 0xff {
			// filter out all chain constrained outputs
			// TODO must be revisited with delegated accounts (delegation-locked on the current sequencer)
			return false
		}
	}
	return true
}

func (tp *Backlog) FilterAndSortOutputs(filter func(wOut vertex.WrappedOutput) bool) []vertex.WrappedOutput {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret := util.KeysFiltered(tp.outputs, filter)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (tp *Backlog) NumOutputsInBuffer() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}

func (tp *Backlog) getStatsAndReset() (ret Stats) {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret = Stats{
		NumOtherSequencers:       tp.NumSequencerTips(),
		NumOutputs:               len(tp.outputs),
		OutputCount:              tp.outputCount,
		RemovedOutputsSinceReset: tp.removedOutputsSinceReset,
	}
	tp.removedOutputsSinceReset = 0
	return
}

func (tp *Backlog) numOutputs() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}
