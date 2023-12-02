package sequencer

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/workflow"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	sequencerTipPool struct {
		mutex                    sync.RWMutex
		glb                      *workflow.Workflow
		accountable              core.Accountable
		outputs                  set.Set[utangle.WrappedOutput]
		log                      *zap.SugaredLogger
		chainID                  core.ChainID
		latestMilestones         map[core.ChainID]*utangle.WrappedTx
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

const fetchLastNTimeSlotsUponStartup = 5

func startTipPool(seqName string, wrk *workflow.Workflow, seqID core.ChainID, logLevel zapcore.Level) (*sequencerTipPool, error) {
	// must be finalized somewhere
	name := fmt.Sprintf("[%sT-%s]", seqName, seqID.StringVeryShort())
	accountAddress := core.CloneAccountable(seqID.AsChainLock())
	ret := &sequencerTipPool{
		glb:              wrk,
		accountable:      accountAddress,
		log:              global.NewLogger(name, logLevel, []string{"stdout"}, global.TimeLayoutDefault),
		outputs:          set.New[utangle.WrappedOutput](),
		chainID:          seqID,
		latestMilestones: make(map[core.ChainID]*utangle.WrappedTx),
	}
	ret.log.Debugf("starting tipPool..")

	ret.mutex.RLock()
	defer ret.mutex.RUnlock()

	// start listening to chain account
	err := wrk.Events().ListenAccount(accountAddress, func(wOut utangle.WrappedOutput) {
		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		ret._clearOrphanedOutputsIfNeeded()
		ret.outputs.Insert(wOut)
		ret.outputCount++
		ret.log.Debugf("IN %s", wOut.IDShort())
	})
	util.AssertNoError(err)

	// start listening to other sequencers
	err = wrk.Events().ListenSequencers(func(vid *utangle.WrappedTx) {
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
	})
	util.AssertNoError(err)

	// fetch all account into tipPool once
	ret.outputs = wrk.UTXOTangle().ScanAccount(accountAddress.AccountID(), fetchLastNTimeSlotsUponStartup)
	return ret, nil
}

const cleanupPeriod = 5 * time.Second

func (tp *sequencerTipPool) _clearOrphanedOutputsIfNeeded() {
	if time.Since(tp.lastPruned.Load()) < cleanupPeriod {
		return
	}
	toDelete := make([]utangle.WrappedOutput, 0)
	for wOut := range tp.outputs {
		wOut.VID.Unwrap(utangle.UnwrapOptions{Deleted: func() {
			toDelete = append(toDelete, wOut)
		}})
	}
	for _, wOut := range toDelete {
		delete(tp.outputs, wOut)
	}
	tp.removedOutputsSinceReset += len(toDelete)
	tp.lastPruned.Store(time.Now())
}

func (tp *sequencerTipPool) filterAndSortOutputs(filter func(o utangle.WrappedOutput) bool) []utangle.WrappedOutput {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	tp._clearOrphanedOutputsIfNeeded()

	ret := util.Keys(tp.outputs, func(o utangle.WrappedOutput) bool {
		return !o.VID.IsDeleted() && filter(o)
	})
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})

	return ret
}

func (tp *sequencerTipPool) ChainID() core.ChainID {
	return tp.chainID
}

func (tp *sequencerTipPool) preSelectAndSortEndorsableMilestones(targetTs core.LogicalTime) []*utangle.WrappedTx {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret := make([]*utangle.WrappedTx, 0)
	for _, ms := range tp.latestMilestones {
		if ms.TimeSlot() != targetTs.TimeSlot() || !core.ValidTimePace(ms.Timestamp(), targetTs) {
			continue
		}
		ret = append(ret, ms)
	}
	sort.Slice(ret, func(i, j int) bool {
		return isPreferredMilestoneAgainstTheOther(tp.glb.UTXOTangle(), ret[i], ret[j]) // order is important !!!
	})
	return ret
}

func (tp *sequencerTipPool) numOutputsInBuffer() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}

func (tp *sequencerTipPool) numOtherMilestones() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.latestMilestones)
}

func (tp *sequencerTipPool) getStatsAndReset() (ret tipPoolStats) {
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

func (tp *sequencerTipPool) numOutputs() int {
	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	return len(tp.outputs)
}
