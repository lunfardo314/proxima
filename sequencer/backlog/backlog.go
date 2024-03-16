package backlog

import (
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

type (
	Environment interface {
		global.NodeGlobal
		ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput))
		SequencerID() ledger.ChainID
		SequencerName() string
		GetLatestMilestone(seqID ledger.ChainID) *vertex.WrappedTx
		LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		NumSequencerTips() int
		BacklogTTLSlots() int
	}

	InputBacklog struct {
		Environment
		mutex                    sync.RWMutex
		outputs                  map[vertex.WrappedOutput]time.Time
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
		outputs:     make(map[vertex.WrappedOutput]time.Time),
	}
	env.Tracef(TraceTag, "starting input backlog for the sequencer %s..", env.SequencerName)

	// start listening to chain-locked account
	env.ListenToAccount(seqID.AsChainLock(), func(wOut vertex.WrappedOutput) {
		env.Tracef(TraceTag, "[%s] output IN: %s", ret.SequencerName, wOut.IDShortString)
		env.TraceTx(&wOut.VID.ID, "[%s] backlog: output #%d IN", ret.SequencerName, wOut.Index)

		if !ret.checkAndReferenceCandidate(wOut) {
			// failed to reference -> ignore
			return
		}
		// referenced
		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		if _, already := ret.outputs[wOut]; already {
			wOut.VID.UnReference()
			env.Tracef(TraceTag, "repeating output %s", wOut.IDShortString)
			env.TraceTx(&wOut.VID.ID, "[%s] output #%d is already in the backlog", ret.SequencerName, wOut.Index)
			return
		}
		ret.outputs[wOut] = time.Now()
		ret.outputCount++
		env.Tracef(TraceTag, "output stored in input backlog: %s (total: %d)", wOut.IDShortString, len(ret.outputs))
		env.TraceTx(&wOut.VID.ID, "[%s] output #%d stored in the backlog", ret.SequencerName, wOut.Index)
	})
	go ret.purgeLoop()
	return ret, nil
}

// checkAndReferenceCandidate if returns false, it is unreferenced, otherwise referenced
func (b *InputBacklog) checkAndReferenceCandidate(wOut vertex.WrappedOutput) bool {
	if wOut.VID.IsBranchTransaction() {
		// outputs of branch transactions are filtered out
		// TODO probably ordinary outputs must not be allowed at ledger constraints level
		b.TraceTx(&wOut.VID.ID, "[%s] backlog::checkAndReferenceCandidate: is branch", b.SequencerName, wOut.Index)
		return false
	}
	if !wOut.VID.Reference() {
		b.TraceTx(&wOut.VID.ID, "[%s] backlog::checkAndReferenceCandidate: failed to reference", b.SequencerName, wOut.Index)
		return false
	}
	if wOut.VID.GetTxStatus() == vertex.Bad {
		wOut.VID.UnReference()
		b.TraceTx(&wOut.VID.ID, "[%s] backlog::checkAndReferenceCandidate: is BAD", b.SequencerName, wOut.Index)
		return false
	}
	o, err := wOut.VID.OutputAt(wOut.Index)
	if err != nil {
		b.TraceTx(&wOut.VID.ID, "[%s] backlog::checkAndReferenceCandidate: OutputAt failed for #%d: %v", b.SequencerName, wOut.Index, err)
		wOut.VID.UnReference()
		return false
	}
	if o != nil {
		if _, idx := o.ChainConstraint(); idx != 0xff {
			// filter out all chain constrained outputs
			// TODO must be revisited with delegated accounts (delegation-locked on the current sequencer)
			b.TraceTx(&wOut.VID.ID, "[%s] backlog::checkAndReferenceCandidate: #%d is chain-constrained", b.SequencerName, wOut.Index)
			wOut.VID.UnReference()
			return false
		}
	}
	// it is referenced
	b.TraceTx(&wOut.VID.ID, "[%s] backlog::checkAndReferenceCandidate: #%d success", b.SequencerName, wOut.Index)
	return true
}

func (b *InputBacklog) CandidatesToEndorseSorted(targetTs ledger.Time) []*vertex.WrappedTx {
	targetSlot := targetTs.Slot()
	ownSeqID := b.SequencerID()
	return b.LatestMilestonesDescending(func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool {
		return vid.Slot() == targetSlot && seqID != ownSeqID
	})
}

func (b *InputBacklog) GetOwnLatestMilestoneTx() *vertex.WrappedTx {
	return b.GetLatestMilestone(b.SequencerID())
}

func (b *InputBacklog) FilterAndSortOutputs(filter func(wOut vertex.WrappedOutput) bool) []vertex.WrappedOutput {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	ret := util.KeysFiltered(b.outputs, filter)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (b *InputBacklog) NumOutputsInBuffer() int {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	return len(b.outputs)
}

func (b *InputBacklog) getStatsAndReset() (ret Stats) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	ret = Stats{
		NumOtherSequencers:       b.NumSequencerTips(),
		NumOutputs:               len(b.outputs),
		OutputCount:              b.outputCount,
		RemovedOutputsSinceReset: b.removedOutputsSinceReset,
	}
	b.removedOutputsSinceReset = 0
	return
}

func (b *InputBacklog) numOutputs() int {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	return len(b.outputs)
}

func (b *InputBacklog) purgeLoop() {
	ttl := time.Duration(b.BacklogTTLSlots()) * ledger.L().ID.SlotDuration()

	for {
		select {
		case <-b.Ctx().Done():
			return

		case <-time.After(time.Second):
			if n := b.purge(ttl); n > 0 {
				b.Log().Infof("purged %d outputs from the backlog", n)
			}
		}
	}
}

func (b *InputBacklog) purge(ttl time.Duration) int {
	horizon := time.Now().Add(-ttl)

	b.mutex.Lock()
	defer b.mutex.Unlock()

	toDelete := make([]vertex.WrappedOutput, 0)
	for wOut, since := range b.outputs {
		if since.Before(horizon) {
			toDelete = append(toDelete, wOut)
		}
	}

	for _, wOut := range toDelete {
		wOut.VID.UnReference()
		delete(b.outputs, wOut)
		b.TraceTx(&wOut.VID.ID, "[%s] output #%d has been deleted from the backlog", b.SequencerName, wOut.Index)
	}
	return len(toDelete)
}
