package factory

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/sequencer/factory/task"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	Environment interface {
		attacher.Environment
		backlog.Environment
		ControllerPrivateKey() ed25519.PrivateKey
		SequencerName() string
		Backlog() *backlog.InputBacklog
		MaxTagAlongOutputs() int
		MilestonesTTLSlots() int
	}

	MilestoneFactory struct {
		Environment
		ownMilestones               map[*vertex.WrappedTx]outputsWithTime // map ms -> consumed outputs in the past
		mutex                       sync.RWMutex
		maxTagAlongInputs           int
		ownMilestoneCount           int
		removedMilestonesSinceReset int
	}

	outputsWithTime struct {
		consumed set.Set[vertex.WrappedOutput]
		since    time.Time
	}

	Stats struct {
		NumOwnMilestones            int
		OwnMilestoneCount           int
		RemovedMilestonesSinceReset int
		backlog.Stats
	}
)

const (
	maxAdditionalOutputs  = 256 - 2 // 1 for chain output, 1 for stem
	veryMaxTagAlongInputs = maxAdditionalOutputs
	TraceTag              = "factory"
)

func New(env Environment) (*MilestoneFactory, error) {
	ret := &MilestoneFactory{
		Environment:       env,
		ownMilestones:     make(map[*vertex.WrappedTx]outputsWithTime),
		maxTagAlongInputs: env.MaxTagAlongOutputs(),
	}
	if ret.maxTagAlongInputs == 0 || ret.maxTagAlongInputs > veryMaxTagAlongInputs {
		ret.maxTagAlongInputs = veryMaxTagAlongInputs
	}
	go ret.purgeLoop()

	env.Tracef(TraceTag, "milestone factory has been created")
	return ret, nil
}

func (mf *MilestoneFactory) isConsumedInThePastPath(wOut vertex.WrappedOutput, ms *vertex.WrappedTx) bool {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	return mf.ownMilestones[ms].consumed.Contains(wOut)
}

func (mf *MilestoneFactory) OwnLatestMilestoneOutput() vertex.WrappedOutput {
	ret := mf.GetLatestMilestone(mf.SequencerID())
	if ret != nil {
		mf.AddOwnMilestone(ret)
		return ret.SequencerWrappedOutput()
	}
	// there's no own milestone in the tippool (startup)
	// find in one of baseline states of other sequencers
	return mf.bootstrapOwnMilestoneOutput()
}

func (mf *MilestoneFactory) bootstrapOwnMilestoneOutput() vertex.WrappedOutput {
	chainID := mf.SequencerID()
	milestones := mf.LatestMilestonesDescending()
	for _, ms := range milestones {
		baseline := ms.BaselineBranch()
		if baseline == nil {
			continue
		}
		rdr := mf.GetStateReaderForTheBranch(&baseline.ID)
		o, err := rdr.GetUTXOForChainID(&chainID)
		if errors.Is(err, multistate.ErrNotFound) {
			continue
		}
		mf.AssertNoError(err)
		ret := attacher.AttachOutputID(o.ID, mf, attacher.OptionInvokedBy("tippool"))
		return ret
	}
	return vertex.WrappedOutput{}
}

func (mf *MilestoneFactory) AddOwnMilestone(vid *vertex.WrappedTx) {
	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	if _, already := mf.ownMilestones[vid]; already {
		return
	}

	vid.MustReference()

	withTime := outputsWithTime{
		consumed: set.New[vertex.WrappedOutput](),
		since:    time.Now(),
	}
	if vid.IsSequencerMilestone() {
		if prev := vid.SequencerPredecessor(); prev != nil {
			if prevConsumed, found := mf.ownMilestones[prev]; found {
				withTime.consumed.AddAll(prevConsumed.consumed)
			}
		}
		vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			v.ForEachInputDependency(func(i byte, vidInput *vertex.WrappedTx) bool {
				withTime.consumed.Insert(vertex.WrappedOutput{
					VID:   vidInput,
					Index: v.Tx.MustOutputIndexOfTheInput(i),
				})
				return true
			})
		}})
	}
	mf.ownMilestones[vid] = withTime
	mf.ownMilestoneCount++
}

func (mf *MilestoneFactory) StartProposingForTargetLogicalTime(targetTs ledger.Time) (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	deadline := targetTs.Time()
	nowis := time.Now()
	mf.Tracef(TraceTag, "StartProposingForTargetLogicalTime: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))

	if deadline.Before(nowis) {
		return nil, nil, fmt.Errorf("target %s is in the past by %v: impossible to generate milestone",
			targetTs.String(), nowis.Sub(deadline))
	}
	return task.Run(mf, targetTs)
}

func (mf *MilestoneFactory) AttachTagAlongInputs(a *attacher.IncrementalAttacher) (numInserted int) {
	mf.Tracef(TraceTag, "AttachTagAlongInputs: %s", a.Name())

	if ledger.L().ID.IsPreBranchConsolidationTimestamp(a.TargetTs()) {
		// skipping tagging-along in pre-branch consolidation zone
		mf.Tracef(TraceTag, "AttachTagAlongInputs: %s. No tag-along in the pre-branch consolidation zone of ticks", a.Name())
		return 0
	}

	preSelected := mf.Backlog().FilterAndSortOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidSequencerPace(wOut.Timestamp(), a.TargetTs()) {
			mf.TraceTx(&wOut.VID.ID, "AttachTagAlongInputs:#%d  not valid pace -> not pre-selected (target %s)", wOut.Index, a.TargetTs().String)
			return false
		}
		// fast filtering out already consumed outputs in the predecessor milestone context
		already := mf.isConsumedInThePastPath(wOut, a.Extending().VID)
		if already {
			mf.TraceTx(&wOut.VID.ID, "AttachTagAlongInputs: #%d already consumed in the past path -> not pre-selected", wOut.Index)
		}
		return !already
	})
	mf.Tracef(TraceTag, "AttachTagAlongInputs %s. Pre-selected: %d", a.Name(), len(preSelected))

	for _, wOut := range preSelected {
		mf.TraceTx(&wOut.VID.ID, "AttachTagAlongInputs: pre-selected #%d", wOut.Index)
		if success, err := a.InsertTagAlongInput(wOut); success {
			numInserted++
			mf.Tracef(TraceTag, "AttachTagAlongInputs %s. Inserted %s", a.Name(), wOut.IDShortString)
			mf.TraceTx(&wOut.VID.ID, "AttachTagAlongInputs %s. Inserted #%d", a.Name(), wOut.Index)
		} else {
			mf.Tracef(TraceTag, "AttachTagAlongInputs %s. Failed to insert %s: '%v'", a.Name(), wOut.IDShortString, err)
			mf.TraceTx(&wOut.VID.ID, "AttachTagAlongInputs %s. Failed to insert #%d: '%v'", a.Name(), wOut.Index, err)
		}
		if a.NumInputs() >= mf.maxTagAlongInputs {
			break
		}
	}
	return
}

func (mf *MilestoneFactory) FutureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs ledger.Time) []vertex.WrappedOutput {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	mf.Tracef(TraceTag, "FutureConeOwnMilestonesOrdered for root output %s. Total %d own milestones", rootOutput.IDShortString, len(mf.ownMilestones))

	_, ok := mf.ownMilestones[rootOutput.VID]
	mf.Assertf(ok, "FutureConeOwnMilestonesOrdered: milestone output %s of chain %s is expected to be among set of own milestones (%d)",
		rootOutput.IDShortString, util.Ref(mf.SequencerID()).StringShort, len(mf.ownMilestones))

	ordered := util.KeysSorted(mf.ownMilestones, func(vid1, vid2 *vertex.WrappedTx) bool {
		// by timestamp -> equivalent to topological order, ascending, i.e. older first
		return vid1.Timestamp().Before(vid2.Timestamp())
	})

	visited := set.New[*vertex.WrappedTx](rootOutput.VID)
	ret := []vertex.WrappedOutput{rootOutput}
	for _, vid := range ordered {
		switch {
		case vid.IsBadOrDeleted():
			continue
		case !vid.IsSequencerMilestone():
			continue
		case !visited.Contains(vid.SequencerPredecessor()):
			continue
		case !ledger.ValidTransactionPace(vid.Timestamp(), targetTs):
			continue
		}
		visited.Insert(vid)
		ret = append(ret, vid.SequencerWrappedOutput())
	}
	return ret
}

func (mf *MilestoneFactory) NumOutputsInBuffer() int {
	return mf.Backlog().NumOutputsInBuffer()
}

func (mf *MilestoneFactory) NumMilestones() int {
	return mf.NumSequencerTips()
}

func (mf *MilestoneFactory) BestCoverageInTheSlot(targetTs ledger.Time) uint64 {
	if targetTs.Tick() == 0 {
		return 0
	}
	all := mf.LatestMilestonesDescending(func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool {
		return vid.Slot() == targetTs.Slot()
	})
	if len(all) == 0 || all[0].IsBranchTransaction() {
		return 0
	}
	return all[0].GetLedgerCoverage()
}

func (mf *MilestoneFactory) purge(ttl time.Duration) int {
	horizon := time.Now().Add(-ttl)

	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	toDelete := make([]*vertex.WrappedTx, 0)
	for vid, withTime := range mf.ownMilestones {
		if withTime.since.Before(horizon) {
			toDelete = append(toDelete, vid)
		}
	}

	for _, vid := range toDelete {
		vid.UnReference()
		delete(mf.ownMilestones, vid)
	}
	return len(toDelete)
}

func (mf *MilestoneFactory) purgeLoop() {
	ttl := time.Duration(mf.MilestonesTTLSlots()) * ledger.L().ID.SlotDuration()

	for {
		select {
		case <-mf.Ctx().Done():
			return

		case <-time.After(time.Second):
			if n := mf.purge(ttl); n > 0 {
				mf.Infof1("purged %d own milestones", n)
			}
		}
	}
}
