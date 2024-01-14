package factory

import (
	"crypto/ed25519"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer"
	"github.com/lunfardo314/proxima/sequencer/tippool"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/zap"
)

type (
	Environment interface {
		Log() *zap.SugaredLogger
		Tracef(tag string, format string, args ...any)
	}

	MilestoneFactory struct {
		mutex                       sync.RWMutex
		name                        string
		env                         Environment
		tipPool                     *tippool.SequencerTipPool
		controllerKey               ed25519.PrivateKey
		proposal                    latestMilestoneProposal
		ownMilestones               map[*vertex.WrappedTx]ownMilestone
		maxTagAlongInputs           int
		lastPruned                  time.Time
		ownMilestoneCount           int
		removedMilestonesSinceReset int
	}

	ownMilestone struct {
		vertex.WrappedOutput
		consumedInThePastPath set.Set[vertex.WrappedOutput]
	}

	proposedMilestoneWithData struct {
		tx         *transaction.Transaction
		extended   vertex.WrappedOutput
		coverage   uint64
		elapsed    time.Duration
		proposedBy string
	}

	latestMilestoneProposal struct {
		mutex             sync.RWMutex
		targetTs          ledger.LogicalTime
		bestSoFarCoverage uint64
		current           *transaction.Transaction
		currentExtended   vertex.WrappedOutput
		durations         []time.Duration
	}

	Stats struct {
		NumOwnMilestones            int
		OwnMilestoneCount           int
		RemovedMilestonesSinceReset int
		tippool.Stats
	}
)

var allProposingStrategies = make(map[string]*proposer.Strategy)

func registerProposerStrategy(s *proposer.Strategy) {
	allProposingStrategies[s.Name] = s
}

const (
	maxAdditionalOutputs    = 256 - 2 // 1 for chain output, 1 for stem
	veryMaxTagAlongInputs   = maxAdditionalOutputs
	cleanupMilestonesPeriod = 1 * time.Second
)

func New(name string, env Environment, tpool *tippool.SequencerTipPool, controllerKey ed25519.PrivateKey, startChainOut vertex.WrappedOutput, maxTagAlongInputs int) *MilestoneFactory {
	ret := &MilestoneFactory{
		name:          name,
		env:           env,
		tipPool:       tpool,
		controllerKey: controllerKey,
		proposal:      latestMilestoneProposal{},
		ownMilestones: map[*vertex.WrappedTx]ownMilestone{
			startChainOut.VID: newOwnMilestone(startChainOut),
		},
		maxTagAlongInputs: maxTagAlongInputs,
	}
	if ret.maxTagAlongInputs == 0 || ret.maxTagAlongInputs > veryMaxTagAlongInputs {
		ret.maxTagAlongInputs = veryMaxTagAlongInputs
	}
	env.Log().Debugf("milestone factory started")
	return ret
}

func newOwnMilestone(wOut vertex.WrappedOutput, inputs ...vertex.WrappedOutput) ownMilestone {
	return ownMilestone{
		WrappedOutput:         wOut,
		consumedInThePastPath: set.New[vertex.WrappedOutput](inputs...),
	}
}

func (mf *MilestoneFactory) isConsumedInThePastPath(wOut vertex.WrappedOutput, ms *vertex.WrappedTx) bool {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	return mf.ownMilestones[ms].consumedInThePastPath.Contains(wOut)
}

func (mf *MilestoneFactory) GetLatestMilestone() (ret vertex.WrappedOutput) {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	for _, ms := range mf.ownMilestones {
		if ret.VID == nil || ms.Timestamp().After(ret.Timestamp()) {
			ret = ms.WrappedOutput
		}
	}
	util.Assertf(ret.VID != nil, "ret.VID != nil")
	return ret
}

func (mf *MilestoneFactory) addOwnMilestone(wOut vertex.WrappedOutput) {
	inputs := wOut.VID.WrappedInputs()
	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	om := newOwnMilestone(wOut, inputs...)
	if wOut.VID.IsSequencerMilestone() {
		if prev := wOut.VID.SequencerPredecessor(); prev != nil {
			if prevOm, found := mf.ownMilestones[prev]; found {
				om.consumedInThePastPath.AddAll(prevOm.consumedInThePastPath)
			}
		}
	}
	if _, found := mf.ownMilestones[wOut.VID]; !found {
		mf.ownMilestones[wOut.VID] = om
		mf.ownMilestoneCount++
	}
}

func (mf *MilestoneFactory) StartProposingForTargetLogicalTime(targetTs ledger.LogicalTime) (*transaction.Transaction, time.Duration, int) {
	deadline := targetTs.Time()
	nowis := time.Now()

	if deadline.Before(nowis) {
		return nil, 0, 0
	}
	// start worker(s)
	mf.setNewTarget(targetTs)
	mf.startProposerWorkers(targetTs)
	// wait util real time deadline
	time.Sleep(deadline.Sub(nowis))

	ret := mf.getLatestProposal() // will return nil if wasn't able to generate transaction
	// set target time to nil -> signal workers to exit
	avgProposalDuration, numProposals := mf.averageProposalDuration()
	mf.resetTarget()
	return ret, avgProposalDuration, numProposals
}

func (mf *MilestoneFactory) Log() *zap.SugaredLogger {
	return mf.env.Log()
}

func (mf *MilestoneFactory) Tracef(tag string, format string, args ...any) {
	mf.env.Tracef(tag, format, args...)
}

// setNewTarget signals proposer allMilestoneProposingStrategies about new timestamp,
// Returns last proposed proposal
func (mf *MilestoneFactory) setNewTarget(ts ledger.LogicalTime) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	mf.proposal.targetTs = ts
	mf.proposal.current = nil
	if ts.IsSlotBoundary() {
		mf.proposal.bestSoFarCoverage = 0
	}
	mf.proposal.durations = make([]time.Duration, 0)
}

func (mf *MilestoneFactory) resetTarget() {
	mf.setNewTarget(ledger.NilLogicalTime)
}

// ContinueCandidateProposing the proposing strategy checks if its assumed target timestamp
// is still actual. Strategy keeps proposing latestMilestone candidates until it is no longer actual
func (mf *MilestoneFactory) ContinueCandidateProposing(ts ledger.LogicalTime) bool {
	mf.proposal.mutex.RLock()
	defer mf.proposal.mutex.RUnlock()

	return mf.proposal.targetTs == ts
}

func (mf *MilestoneFactory) AttachTagAlongInputs(a *attacher.IncrementalAttacher, targetTs ledger.LogicalTime) {
	preSelected := mf.tipPool.FilterAndSortOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidTimePace(wOut.Timestamp(), targetTs) {
			return false
		}
		// fast filtering out already consumed outputs in the predecessor milestone context
		return !mf.isConsumedInThePastPath(wOut, a.Extending())
	})
	for _, wOut := range preSelected {
		a.InsertTagAlongInput(wOut, set.New[*vertex.WrappedTx]())
		if a.NumInputs() >= mf.maxTagAlongInputs {
			break
		}
	}
	return
}

func (mf *MilestoneFactory) startProposerWorkers(targetTime ledger.LogicalTime) {
	for strategyName, s := range allProposingStrategies {
		task := proposer.New(mf, s, targetTime)
		if task != nil {
			mf.env.Tracef("proposer", "RUN '%s' proposer for the target %s", strategyName, targetTime.String())
			util.RunWrappedRoutine(mf.name+"::"+task.Name(), func() {
				mf.Tracef("proposer", " START proposer %s", task.Name())
				task.Run()
				mf.Tracef("proposer", " END proposer %s", task.Name())
			}, func(err error) {
				mf.env.Log().Fatal(err)
			},
				common.ErrDBUnavailable)
		} else {
			mf.Tracef("proposer", "SKIP '%s' proposer for the target %s", strategyName, targetTime.String())
		}
	}
}

func (mf *MilestoneFactory) getLatestProposal() *transaction.Transaction {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	return mf.proposal.current
}

func (mf *MilestoneFactory) StoreProposalDuration(d time.Duration) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	mf.proposal.durations = append(mf.proposal.durations, d)
}

func (mf *MilestoneFactory) averageProposalDuration() (time.Duration, int) {
	mf.proposal.mutex.RLock()
	defer mf.proposal.mutex.RUnlock()

	if len(mf.proposal.durations) == 0 {
		return 0, 0
	}
	sum := int64(0)
	for _, d := range mf.proposal.durations {
		sum += int64(d)
	}
	return time.Duration(sum / int64(len(mf.proposal.durations))), len(mf.proposal.durations)
}

func (mf *MilestoneFactory) cleanOwnMilestonesIfNecessary() {
	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	if time.Since(mf.lastPruned) < cleanupMilestonesPeriod {
		return
	}

	toDelete := make([]*vertex.WrappedTx, 0)
	for vid := range mf.ownMilestones {
		if vid.IsBadOrDeleted() {
			toDelete = append(toDelete, vid)
		}
	}
	for _, vid := range toDelete {
		delete(mf.ownMilestones, vid)
	}
	mf.removedMilestonesSinceReset += len(toDelete)
}
