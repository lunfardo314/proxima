package sequencer_old

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/utangle_old"
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
		accountable              ledger.Accountable
		outputs                  set.Set[utangle_old.WrappedOutput]
		log                      *zap.SugaredLogger
		chainID                  ledger.ChainID
		latestMilestones         map[ledger.ChainID]*utangle_old.WrappedTx
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

func startTipPool(seqName string, wrk *workflow.Workflow, seqID ledger.ChainID, logLevel zapcore.Level) (*sequencerTipPool, error) {
	// must be finalized somewhere
	name := fmt.Sprintf("[%sT-%s]", seqName, seqID.StringVeryShort())
	accountAddress := ledger.CloneAccountable(seqID.AsChainLock())
	ret := &sequencerTipPool{
		glb:              wrk,
		accountable:      accountAddress,
		log:              global.NewLogger(name, logLevel, []string{"stdout"}, global.TimeLayoutDefault),
		outputs:          set.New[utangle_old.WrappedOutput](),
		chainID:          seqID,
		latestMilestones: make(map[ledger.ChainID]*utangle_old.WrappedTx),
	}
	ret.log.Debugf("starting tipPool..")

	ret.mutex.RLock()
	defer ret.mutex.RUnlock()

	// start listening to chain account
	err := wrk.Events().ListenAccount(accountAddress, func(wOut utangle_old.WrappedOutput) {
		ret.purgeDeleted()

		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		ret.outputs.Insert(wOut)
		ret.outputCount++
		ret.log.Debugf("IN %s", wOut.IDShort())
	})
	util.AssertNoError(err)

	// start listening to other sequencers
	err = wrk.Events().ListenSequencers(func(vid *utangle_old.WrappedTx) {
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

func (tp *sequencerTipPool) purgeDeleted() {
	cleanupPeriod := ledger.SlotDuration() / 2
	if time.Since(tp.lastPruned.Load()) < cleanupPeriod {
		return
	}

	tp.mutex.Lock()
	defer tp.mutex.Unlock()

	toDelete := make([]utangle_old.WrappedOutput, 0)
	for wOut := range tp.outputs {
		wOut.VID.Unwrap(utangle_old.UnwrapOptions{Deleted: func() {
			toDelete = append(toDelete, wOut)
		}})
	}
	for _, wOut := range toDelete {
		delete(tp.outputs, wOut)
	}
	tp.removedOutputsSinceReset += len(toDelete)

	toDeleteMilestoneChainID := make([]ledger.ChainID, 0)
	for chainID, vid := range tp.latestMilestones {
		if vid.IsDeleted() {
			toDeleteMilestoneChainID = append(toDeleteMilestoneChainID, chainID)
		}
	}
	for i := range toDeleteMilestoneChainID {
		delete(tp.latestMilestones, toDeleteMilestoneChainID[i])
	}
	tp.lastPruned.Store(time.Now())
}

func (tp *sequencerTipPool) filterAndSortOutputs(filter func(o utangle_old.WrappedOutput) bool) []utangle_old.WrappedOutput {
	tp.purgeDeleted()

	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret := util.KeysFiltered(tp.outputs, func(o utangle_old.WrappedOutput) bool {
		return !o.VID.IsDeleted() && filter(o)
	})
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})

	return ret
}

func (tp *sequencerTipPool) ChainID() ledger.ChainID {
	return tp.chainID
}

func (tp *sequencerTipPool) preSelectAndSortEndorsableMilestones(targetTs ledger.Time) []*utangle_old.WrappedTx {
	tp.purgeDeleted()

	tp.mutex.RLock()
	defer tp.mutex.RUnlock()

	ret := make([]*utangle_old.WrappedTx, 0)
	for _, ms := range tp.latestMilestones {
		if ms.TimeSlot() != targetTs.Slot() || !ledger.ValidTransactionPace(ms.Timestamp(), targetTs) {
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
