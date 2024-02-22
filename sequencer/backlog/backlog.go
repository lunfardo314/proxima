package backlog

import (
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
		PullSequencerTips(seqID ledger.ChainID) (set.Set[vertex.WrappedOutput], error)
		SequencerID() ledger.ChainID
		SequencerName() string
		GetLatestMilestone(seqID ledger.ChainID) *vertex.WrappedTx
		LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		NumSequencerTips() int
	}

	InputBacklog struct {
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

const TraceTag = "backlog"

func New(env Environment) (*InputBacklog, error) {
	seqID := env.SequencerID()
	ret := &InputBacklog{
		Environment: env,
		outputs:     set.New[vertex.WrappedOutput](),
	}
	env.Tracef(TraceTag, "starting input backlog for the sequencer %s..", env.SequencerName)

	ret.mutex.RLock()
	defer ret.mutex.RUnlock()

	// start listening to chain-locked account
	env.ListenToAccount(seqID.AsChainLock(), func(wOut vertex.WrappedOutput) {
		env.Tracef(TraceTag, "[%s] output IN: %s", ret.SequencerName, wOut.IDShortString)

		if !checkAndReferenceCandidate(wOut) {
			// failed to reference -> ignore
			return
		}
		// referenced
		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		if !ret.outputs.InsertNew(wOut) {
			// repeating
			wOut.VID.UnReference()
			env.Tracef(TraceTag, "repeating output %s", wOut.IDShortString)
			return
		}
		ret.outputCount++
		env.Tracef(TraceTag, "output stored in input backlog: %s (total: %d)", wOut.IDShortString, len(ret.outputs))
	})

	// fetch backlog and milestone once
	var err error
	ret.outputs, err = env.PullSequencerTips(seqID)
	if err != nil {
		return nil, err
	}
	env.Tracef(TraceTag, "PullSequencerTips: loaded %d outputs", len(ret.outputs))
	return ret, nil
}

// checkAndReferenceCandidate if returns false, it is unreferenced, otherwise referenced
func checkAndReferenceCandidate(wOut vertex.WrappedOutput) bool {
	if wOut.VID.IsBranchTransaction() {
		// outputs of branch transactions are filtered out
		// TODO probably ordinary outputs must not be allowed at ledger constraints level
		return false
	}
	if !wOut.VID.Reference() {
		return false
	}
	if wOut.VID.GetTxStatus() == vertex.Bad {
		wOut.VID.UnReference()
		return false
	}
	o, err := wOut.VID.OutputAt(wOut.Index)
	if err != nil {
		wOut.VID.UnReference()
		return false
	}
	if o != nil {
		if _, idx := o.ChainConstraint(); idx != 0xff {
			// filter out all chain constrained outputs
			// TODO must be revisited with delegated accounts (delegation-locked on the current sequencer)
			wOut.VID.UnReference()
			return false
		}
	}
	// it is referenced
	return true
}

func (tp *InputBacklog) CandidatesToEndorseSorted(targetTs ledger.Time) []*vertex.WrappedTx {
	targetSlot := targetTs.Slot()
	ownSeqID := tp.SequencerID()
	return tp.LatestMilestonesDescending(func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool {
		return vid.Slot() == targetSlot && seqID != ownSeqID
	})
}

func (tp *InputBacklog) GetOwnLatestMilestoneTx() *vertex.WrappedTx {
	return tp.GetLatestMilestone(tp.SequencerID())
}

func (tp *InputBacklog) FilterAndSortOutputs(filter func(wOut vertex.WrappedOutput) bool) []vertex.WrappedOutput {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret := util.KeysFiltered(tp.outputs, filter)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (tp *InputBacklog) NumOutputsInBuffer() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}

func (tp *InputBacklog) getStatsAndReset() (ret Stats) {
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

func (tp *InputBacklog) numOutputs() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}
