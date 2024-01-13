package tippool

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

type (
	ListenEnvironment interface {
		ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput))
		ListenToSequencers(fun func(vid *vertex.WrappedTx))
		ScanAccount(addr ledger.AccountID) set.Set[vertex.WrappedOutput]
		Log() *zap.SugaredLogger
		Tracef(tag string, format string, args ...any)
	}

	SequencerTipPool struct {
		mutex                    sync.RWMutex
		env                      ListenEnvironment
		outputs                  set.Set[vertex.WrappedOutput]
		seqID                    ledger.ChainID
		name                     string
		latestMilestones         map[ledger.ChainID]*vertex.WrappedTx
		lastPruned               atomic.Time
		outputCount              int
		removedOutputsSinceReset int
	}

	tipPoolStats struct {
		numOtherSequencers       int
		numOutputs               int
		outputCount              int
		removedOutputsSinceReset int
	}
)

// TODO tag-along and delegation locks

func Start(env ListenEnvironment, seqID ledger.ChainID, namePrefix string) *SequencerTipPool {
	accountAddress := ledger.CloneAccountable(seqID.AsChainLock())
	ret := &SequencerTipPool{
		env:              env,
		outputs:          set.New[vertex.WrappedOutput](),
		seqID:            seqID,
		name:             fmt.Sprintf("%s-%s", namePrefix, seqID.StringVeryShort()),
		latestMilestones: make(map[ledger.ChainID]*vertex.WrappedTx),
	}
	env.Tracef("tippool", "starting tipPool..")

	ret.mutex.RLock()
	defer ret.mutex.RUnlock()

	// start listening to chain account
	env.ListenToAccount(accountAddress, func(wOut vertex.WrappedOutput) {
		ret.purge()
		if isOutputToDelete(wOut) {
			return
		}

		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		ret.outputs.Insert(wOut)
		ret.outputCount++
		env.Tracef("tippool", "[%s] output IN: %s", ret.name, wOut.IDShortString)
	})

	// start listening to other sequencers
	env.ListenToSequencers(func(vid *vertex.WrappedTx) {
		seqIDIncoming, ok := vid.SequencerIDIfAvailable()
		util.Assertf(ok, "sequencer milestone expected")

		if seqIDIncoming == seqID {
			return
		}

		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		old, prevExists := ret.latestMilestones[seqIDIncoming]
		if !prevExists || !vid.Timestamp().Before(old.Timestamp()) {
			ret.latestMilestones[seqIDIncoming] = vid
		}
		env.Tracef("tippool", "[%s] milestone IN: %s", ret.name, vid.IDShortString)
	})

	// fetch all account into tipPool once
	ret.outputs = env.ScanAccount(accountAddress.AccountID())
	return ret
}

func isOutputToDelete(wOut vertex.WrappedOutput) bool {
	if wOut.VID.IsBadOrDeleted() {
		return true
	}
	if wOut.VID.IsBranchTransaction() {
		// outputs of branch transactions are filtered out
		// TODO probably ordinary outputs must not be allowed at ledger level
		return true
	}
	o, err := wOut.VID.OutputAt(wOut.Index)
	if err != nil {
		return true
	}
	if o != nil {
		if _, idx := o.ChainConstraint(); idx != 0xff {
			// filter out all chain constrained outputs
			// TODO must be revisited with delegated accounts (delegation-locked on the current sequencer)
			return true
		}
	}
	return false
}

// TODO purge also bad
func (tp *SequencerTipPool) purge() {
	cleanupPeriod := ledger.SlotDuration() / 2
	if time.Since(tp.lastPruned.Load()) < cleanupPeriod {
		return
	}

	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	toDelete := make([]vertex.WrappedOutput, 0)
	for wOut := range tp.outputs {
		if isOutputToDelete(wOut) {
			toDelete = append(toDelete, wOut)
		}
	}
	for _, wOut := range toDelete {
		delete(tp.outputs, wOut)
	}
	tp.removedOutputsSinceReset += len(toDelete)

	toDeleteMilestoneChainID := make([]ledger.ChainID, 0)
	for chainID, vid := range tp.latestMilestones {
		if vid.IsBadOrDeleted() {
			toDeleteMilestoneChainID = append(toDeleteMilestoneChainID, chainID)
		}
	}
	for i := range toDeleteMilestoneChainID {
		delete(tp.latestMilestones, toDeleteMilestoneChainID[i])
	}
	tp.lastPruned.Store(time.Now())
}

func (tp *SequencerTipPool) filterAndSortOutputs(filter func(o vertex.WrappedOutput) bool) []vertex.WrappedOutput {
	tp.purge()

	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret := util.Keys(tp.outputs, filter)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (tp *SequencerTipPool) preSelectAndSortEndorsableMilestones(targetTs ledger.LogicalTime) []*vertex.WrappedTx {
	tp.purge()

	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret := make([]*vertex.WrappedTx, 0)
	for _, ms := range tp.latestMilestones {
		if ms.Slot() != targetTs.Slot() || !ledger.ValidTimePace(ms.Timestamp(), targetTs) {
			continue
		}
		ret = append(ret, ms)
	}
	sort.Slice(ret, func(i, j int) bool {
		return isPreferredMilestoneAgainstTheOther(ret[i], ret[j]) // order is important !!!
	})
	return ret
}

// betterMilestone returns if vid1 is strongly better than vid2
func isPreferredMilestoneAgainstTheOther(vid1, vid2 *vertex.WrappedTx) bool {
	util.Assertf(vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone(), "vid1.IsSequencerMilestone() && vid2.IsSequencerMilestone()")

	if vid1 == vid2 {
		return false
	}
	if vid2 == nil {
		return true
	}

	coverage1 := vid1.GetLedgerCoverage().Sum()
	coverage2 := vid2.GetLedgerCoverage().Sum()
	switch {
	case coverage1 > coverage2:
		// main preference is by ledger coverage
		return true
	case coverage1 == coverage2:
		// in case of equal coverage hash will be used
		return bytes.Compare(vid1.ID[:], vid2.ID[:]) > 0
	default:
		return false
	}
}

func (tp *SequencerTipPool) numOutputsInBuffer() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}

func (tp *SequencerTipPool) numOtherMilestones() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.latestMilestones)
}

func (tp *SequencerTipPool) getStatsAndReset() (ret tipPoolStats) {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret = tipPoolStats{
		numOtherSequencers:       len(tp.latestMilestones),
		numOutputs:               len(tp.outputs),
		outputCount:              tp.outputCount,
		removedOutputsSinceReset: tp.removedOutputsSinceReset,
	}
	tp.removedOutputsSinceReset = 0
	return
}

func (tp *SequencerTipPool) numOutputs() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}
