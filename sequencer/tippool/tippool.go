package tippool

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/atomic"
)

type (
	Environment interface {
		global.Logging
		attacher.Environment
		ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput))
		ListenToSequencers(fun func(vid *vertex.WrappedTx))
		PullSequencerTips(seqID ledger.ChainID, loadOwnMilestones bool) (set.Set[vertex.WrappedOutput], error)
		SequencerID() ledger.ChainID
		GetLatestMilestone(seqID ledger.ChainID) *vertex.WrappedTx
		LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		NumSequencerTips() int
	}

	SequencerTipPool struct {
		Environment
		mutex                    sync.RWMutex
		outputs                  set.Set[vertex.WrappedOutput]
		name                     string
		lastPruned               atomic.Time
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
	TraceTag                     = "tippool"
	OptionDoNotLoadOwnMilestones = Option(iota)
)

func New(env Environment, namePrefix string, opts ...Option) (*SequencerTipPool, error) {
	seqID := env.SequencerID()
	ret := &SequencerTipPool{
		Environment: env,
		outputs:     set.New[vertex.WrappedOutput](),
		name:        fmt.Sprintf("%s-%s", namePrefix, seqID.StringVeryShort()),
	}
	env.Tracef(TraceTag, "starting tipPool..")

	ret.mutex.RLock()
	defer ret.mutex.RUnlock()

	// start listening to chain-locked account
	env.ListenToAccount(seqID.AsChainLock(), func(wOut vertex.WrappedOutput) {
		env.Tracef(TraceTag, "[%s] output IN: %s", ret.name, wOut.IDShortString)
		ret.purge()

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

func (tp *SequencerTipPool) CandidatesToEndorseSorted(targetTs ledger.Time) []*vertex.WrappedTx {
	targetSlot := targetTs.Slot()
	ownSeqID := tp.SequencerID()
	return tp.LatestMilestonesDescending(func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool {
		return vid.Slot() == targetSlot && seqID != ownSeqID
	})
}

func (tp *SequencerTipPool) GetOwnLatestMilestoneTx() *vertex.WrappedTx {
	return tp.GetLatestMilestone(tp.SequencerID())
}

func (tp *SequencerTipPool) GetOwnLatestMilestoneOutput() vertex.WrappedOutput {
	ret := tp.GetLatestMilestone(tp.SequencerID())
	if ret != nil {
		ret.SequencerWrappedOutput()
	}

	// there's no own milestone in the tippool (startup)
	// find in one of baseline states of other sequencers
	return tp.bootstrapOwnMilestoneOutput()
}

func (tp *SequencerTipPool) bootstrapOwnMilestoneOutput() vertex.WrappedOutput {
	chainID := tp.SequencerID()
	milestones := tp.LatestMilestonesDescending()
	for _, ms := range milestones {
		baseline := ms.BaselineBranch()
		if baseline == nil {
			continue
		}
		rdr := tp.GetStateReaderForTheBranch(baseline)
		o, err := rdr.GetUTXOForChainID(&chainID)
		if errors.Is(err, multistate.ErrNotFound) {
			continue
		}
		util.AssertNoError(err)
		ret := attacher.AttachOutputID(o.ID, tp, attacher.OptionInvokedBy("tippool"))
		return ret
	}
	return vertex.WrappedOutput{}
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

// TODO purge also bad ones
func (tp *SequencerTipPool) purge() {
	cleanupPeriod := ledger.SlotDuration() / 5
	if time.Since(tp.lastPruned.Load()) < cleanupPeriod {
		return
	}

	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	toDelete := make([]vertex.WrappedOutput, 0)
	for wOut := range tp.outputs {
		if !isCandidateToTagAlong(wOut) {
			toDelete = append(toDelete, wOut)
		}
	}
	for _, wOut := range toDelete {
		tp.Tracef(TraceTag, "output deleted from tippool: %s", wOut.IDShortString)
		delete(tp.outputs, wOut)
	}
	tp.removedOutputsSinceReset += len(toDelete)
	tp.lastPruned.Store(time.Now())
}

func (tp *SequencerTipPool) FilterAndSortOutputs(filter func(wOut vertex.WrappedOutput) bool) []vertex.WrappedOutput {
	tp.purge()

	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret := util.KeysFiltered(tp.outputs, filter)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (tp *SequencerTipPool) NumOutputsInBuffer() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}

func (tp *SequencerTipPool) NumMilestones() int {
	return tp.NumSequencerTips()
}

func (tp *SequencerTipPool) getStatsAndReset() (ret Stats) {
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

func (tp *SequencerTipPool) numOutputs() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}

func (tp *SequencerTipPool) BestMilestoneInTheSlot(slot ledger.Slot) *vertex.WrappedTx {
	all := tp.LatestMilestonesDescending(func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool {
		return vid.Slot() == slot
	})
	if len(all) == 0 {
		return nil
	}
	return all[0]
}
