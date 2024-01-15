package factory

import (
	"crypto/ed25519"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_base"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
	"github.com/lunfardo314/proxima/sequencer/tippool"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
)

type (
	Environment interface {
		attacher.Environment
		tippool.Environment
		ControllerPrivateKey() ed25519.PrivateKey
		SequencerName() string
		SequencerID() ledger.ChainID
	}

	MilestoneFactory struct {
		Environment
		mutex                       sync.RWMutex
		name                        string
		tipPool                     *tippool.SequencerTipPool
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

	latestMilestoneProposal struct {
		mutex             sync.RWMutex
		targetTs          ledger.LogicalTime
		bestSoFarCoverage uint64
		current           *transaction.Transaction
		currentExtended   vertex.WrappedOutput
	}

	proposedMilestoneWithData struct {
		tx         *transaction.Transaction
		extended   vertex.WrappedOutput
		coverage   uint64
		elapsed    time.Duration
		proposedBy string
	}

	Stats struct {
		NumOwnMilestones            int
		OwnMilestoneCount           int
		RemovedMilestonesSinceReset int
		tippool.Stats
	}
)

var allProposingStrategies = make(map[string]*proposer_generic.Strategy)

func registerProposerStrategy(s *proposer_generic.Strategy) {
	allProposingStrategies[s.Name] = s
}

func init() {
	registerProposerStrategy(proposer_base.Strategy())
}

const (
	maxAdditionalOutputs    = 256 - 2 // 1 for chain output, 1 for stem
	veryMaxTagAlongInputs   = maxAdditionalOutputs
	cleanupMilestonesPeriod = 1 * time.Second
)

func New(name string, env Environment, maxTagAlongInputs int) (*MilestoneFactory, error) {
	ret := &MilestoneFactory{
		Environment:       env,
		name:              name,
		proposal:          latestMilestoneProposal{},
		ownMilestones:     map[*vertex.WrappedTx]ownMilestone{},
		maxTagAlongInputs: maxTagAlongInputs,
	}
	if ret.maxTagAlongInputs == 0 || ret.maxTagAlongInputs > veryMaxTagAlongInputs {
		ret.maxTagAlongInputs = veryMaxTagAlongInputs
	}
	var err error
	ret.tipPool, err = tippool.New(ret, name)
	if err != nil {
		return nil, err
	}
	env.Log().Debugf("milestone factory started")
	return ret, nil
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

func (mf *MilestoneFactory) OwnLatestMilestone() (ret vertex.WrappedOutput) {
	latest := mf.tipPool.GetOwnLatestMilestoneTx()
	util.Assertf(latest != nil, "cannot find own latest milestone")

	mf.addOwnMilestone(latest.SequencerWrappedOutput())
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

func (mf *MilestoneFactory) StartProposingForTargetLogicalTime(targetTs ledger.LogicalTime) *transaction.Transaction {
	deadline := targetTs.Time()
	nowis := time.Now()

	if deadline.Before(nowis) {
		return nil
	}
	// start worker(s)
	mf.setNewTarget(targetTs)
	mf.startProposerWorkers(targetTs)
	// wait util real time deadline
	time.Sleep(deadline.Sub(nowis))

	mf.resetTarget()
	return mf.getLatestProposal() // will return nil if wasn't able to generate transaction
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

func (mf *MilestoneFactory) AttachTagAlongInputs(a *attacher.IncrementalAttacher) {
	preSelected := mf.tipPool.FilterAndSortOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidTimePace(wOut.Timestamp(), a.TargetTs()) {
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
		task := proposer_generic.New(mf, s, targetTime)
		if task == nil {
			mf.Tracef("proposer", "SKIP '%s' proposer for the target %s", strategyName, targetTime.String())
			continue
		}
		mf.Tracef("proposer", "RUN '%s' proposer for the target %s", strategyName, targetTime.String())

		util.RunWrappedRoutine(mf.name+"::"+task.Name(), func() {
			mf.Tracef("proposer", " START proposer %s", task.Name())
			task.Run()
			mf.Tracef("proposer", " END proposer %s", task.Name())
		}, func(err error) {
			mf.Log().Fatal(err)
		}, common.ErrDBUnavailable, vertex.ErrDeletedVertexAccessed)
	}
}

func (mf *MilestoneFactory) Propose(a *attacher.IncrementalAttacher) (forceExit bool) {
	coverage := a.LedgerCoverageSum()

	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	if coverage <= mf.proposal.bestSoFarCoverage {
		mf.Tracef("proposer", "proposal %s rejected due no increase in coverage", a.Name())
		return
	}

	tx, err := a.MakeTransaction(mf.SequencerName(), mf.ControllerPrivateKey())
	if err != nil {
		mf.Log().Warnf("proposer '%s': error during transaction generation: '%v'", a.Name(), err)
		return true
	}

	mf.proposal.current = tx
	mf.proposal.bestSoFarCoverage = coverage
	mf.proposal.currentExtended = a.ExtendedOutput()
	return
}

func (mf *MilestoneFactory) getLatestProposal() *transaction.Transaction {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	return mf.proposal.current
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
