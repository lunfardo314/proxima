package sequencer

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/workflow"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// TODO rewrite mempool only with IDs, no pointers

type sequencerTipPool struct {
	mutex            sync.RWMutex
	glb              *workflow.Workflow
	accountable      core.Accountable
	outputs          set.Set[utangle.WrappedOutput]
	log              *zap.SugaredLogger
	chainID          core.ChainID
	latestMilestones map[core.ChainID]*utangle.WrappedTx
	lastPruned       time.Time
}

const fetchLastNTimeSlotsUponStartup = 5

func startMempool(seqName string, wrk *workflow.Workflow, seqID core.ChainID, logLevel zapcore.Level) (*sequencerTipPool, error) {
	// must be finalized somewhere
	name := fmt.Sprintf("[%sM-%s]", seqName, seqID.VeryShort())
	accountAddress := core.CloneAccountable(seqID.AsChainLock())
	ret := &sequencerTipPool{
		glb:              wrk,
		accountable:      accountAddress,
		log:              testutil.NewNamedLogger(name, logLevel),
		outputs:          set.New[utangle.WrappedOutput](),
		chainID:          seqID,
		latestMilestones: make(map[core.ChainID]*utangle.WrappedTx),
	}
	ret.log.Debugf("starting mempool..")

	ret.mutex.RLock()
	defer ret.mutex.RUnlock()

	// start listening to chain account
	err := wrk.Events().ListenAccount(accountAddress, func(wOut utangle.WrappedOutput) {
		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		ret._clearOrphanedOutputsIfNeeded()
		ret.outputs.Insert(wOut)
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

	// fetch all account into mempool once
	ret.outputs = wrk.UTXOTangle().ScanAccount(accountAddress.AccountID(), fetchLastNTimeSlotsUponStartup)
	return ret, nil
}

const cleanupPeriod = 1 * time.Second

func (mem *sequencerTipPool) _clearOrphanedOutputsIfNeeded() {
	if time.Since(mem.lastPruned) < cleanupPeriod {
		return
	}
	toDelete := make([]utangle.WrappedOutput, 0)
	for wOut := range mem.outputs {
		wOut.VID.Unwrap(utangle.UnwrapOptions{Orphaned: func() {
			toDelete = append(toDelete, wOut)
		}})
	}
	for _, wOut := range toDelete {
		mem.log.Infof("removed orphaned output %s from tippool", wOut.IDShort())
		delete(mem.outputs, wOut)
	}
	mem.lastPruned = time.Now()
}

func (mem *sequencerTipPool) filterAndSortOutputs(filter func(o utangle.WrappedOutput) bool) []utangle.WrappedOutput {
	mem.mutex.RLock()
	defer mem.mutex.RUnlock()

	mem._clearOrphanedOutputsIfNeeded()

	ret := util.Keys(mem.outputs, func(o utangle.WrappedOutput) bool {
		return !o.VID.IsOrphaned() && filter(o)
	})
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})

	return ret
}

func (mem *sequencerTipPool) ChainID() core.ChainID {
	return mem.chainID
}

func (mem *sequencerTipPool) getHeaviestAnotherMilestoneToEndorse(targetTs core.LogicalTime) *utangle.WrappedTx {
	mem.mutex.RLock()
	defer mem.mutex.RUnlock()

	var ret *utangle.WrappedTx

	for _, ms := range mem.latestMilestones {
		switch {
		case ms.TimeSlot() != targetTs.TimeSlot() || !core.ValidTimePace(ms.Timestamp(), targetTs):
			// must be in the same time slot and with valid time pace
			continue
		case ret == nil:
			ret = ms
		case isPreferredMilestoneAgainstTheOther(ms, ret):
			ret = ms
		}
	}
	return ret
}

func (mem *sequencerTipPool) numOutputsInBuffer() int {
	mem.mutex.RLock()
	defer mem.mutex.RUnlock()

	return len(mem.outputs)
}

func (mem *sequencerTipPool) numOtherMilestones() int {
	mem.mutex.RLock()
	defer mem.mutex.RUnlock()

	return len(mem.latestMilestones)
}
