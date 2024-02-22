package factory

import (
	"context"
	"crypto/ed25519"
	"errors"
	"runtime/debug"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_base"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_endorse1"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	Environment interface {
		attacher.Environment
		backlog.Environment
		ControllerPrivateKey() ed25519.PrivateKey
		SequencerName() string
		MaxTagAlongOutputs() int
		Backlog() *backlog.InputBacklog
	}

	MilestoneFactory struct {
		Environment
		mutex                       sync.RWMutex
		proposal                    latestMilestoneProposal
		ownMilestones               map[*vertex.WrappedTx]set.Set[vertex.WrappedOutput] // map ms -> consumed outputs in the past
		maxTagAlongInputs           int
		lastPruned                  time.Time
		ownMilestoneCount           int
		removedMilestonesSinceReset int
	}

	latestMilestoneProposal struct {
		mutex             sync.RWMutex
		targetTs          ledger.Time
		bestSoFarCoverage multistate.LedgerCoverage
		current           *transaction.Transaction
		currentExtended   vertex.WrappedOutput
	}

	Stats struct {
		NumOwnMilestones            int
		OwnMilestoneCount           int
		RemovedMilestonesSinceReset int
		backlog.Stats
	}
)

var allProposingStrategies = make(map[string]*proposer_generic.Strategy)

func registerProposerStrategy(s *proposer_generic.Strategy) {
	allProposingStrategies[s.Name] = s
}

func init() {
	registerProposerStrategy(proposer_base.Strategy())
	registerProposerStrategy(proposer_endorse1.Strategy())
}

const (
	maxAdditionalOutputs    = 256 - 2 // 1 for chain output, 1 for stem
	veryMaxTagAlongInputs   = maxAdditionalOutputs
	cleanupMilestonesPeriod = 1 * time.Second
	TraceTag                = "factory"
)

func New(env Environment) (*MilestoneFactory, error) {
	ret := &MilestoneFactory{
		Environment:       env,
		proposal:          latestMilestoneProposal{},
		ownMilestones:     make(map[*vertex.WrappedTx]set.Set[vertex.WrappedOutput]),
		maxTagAlongInputs: env.MaxTagAlongOutputs(),
	}
	if ret.maxTagAlongInputs == 0 || ret.maxTagAlongInputs > veryMaxTagAlongInputs {
		ret.maxTagAlongInputs = veryMaxTagAlongInputs
	}
	env.Tracef(TraceTag, "milestone factory has been created")
	return ret, nil
}

func (mf *MilestoneFactory) isConsumedInThePastPath(wOut vertex.WrappedOutput, ms *vertex.WrappedTx) bool {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	return mf.ownMilestones[ms].Contains(wOut)
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
		util.AssertNoError(err)
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

	consumed := set.New[vertex.WrappedOutput]()
	if vid.IsSequencerMilestone() {
		if prev := vid.SequencerPredecessor(); prev != nil {
			if prevConsumed, found := mf.ownMilestones[prev]; found {
				consumed.AddAll(prevConsumed)
			}
		}
		vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			v.ForEachInputDependency(func(i byte, vidInput *vertex.WrappedTx) bool {
				consumed.Insert(vertex.WrappedOutput{
					VID:   vidInput,
					Index: v.Tx.MustOutputIndexOfTheInput(i),
				})
				return true
			})
		}})
	}
	mf.ownMilestones[vid] = consumed
	mf.ownMilestoneCount++
}

func (mf *MilestoneFactory) StartProposingForTargetLogicalTime(targetTs ledger.Time) *transaction.Transaction {
	deadline := targetTs.Time()
	nowis := time.Now()
	mf.Tracef(TraceTag, "StartProposingForTargetLogicalTime: targetTs: %v, nowis: %v", deadline, nowis)

	if deadline.Before(nowis) {
		mf.Tracef(TraceTag, "deadline is in the past: impossible to generate milestone")
		return nil
	}
	// start worker(s)
	mf.setNewTarget(targetTs)
	ctx, cancel := context.WithDeadline(mf.Ctx(), deadline)
	defer cancel() // to prevent context leak
	mf.startProposerWorkers(targetTs, ctx)

	<-ctx.Done()

	ret := mf.getLatestProposal() // will return nil if wasn't able to generate transaction
	return ret
}

// setNewTarget signals proposer allMilestoneProposingStrategies about new timestamp,
// Returns last proposed proposal
func (mf *MilestoneFactory) setNewTarget(ts ledger.Time) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	if mf.proposal.targetTs.IsSlotBoundary() || mf.proposal.targetTs.Slot() != ts.Slot() {
		// if starting new slot, reset best coverage delta
		mf.proposal.bestSoFarCoverage = multistate.LedgerCoverage{}
	}
	mf.proposal.targetTs = ts
	mf.proposal.current = nil
}

func (mf *MilestoneFactory) CurrentTargetTs() ledger.Time {
	mf.proposal.mutex.RLock()
	defer mf.proposal.mutex.RUnlock()

	return mf.proposal.targetTs
}

func (mf *MilestoneFactory) AttachTagAlongInputs(a *attacher.IncrementalAttacher) (numInserted int) {
	mf.Tracef(TraceTag, "AttachTagAlongInputs: %s", a.Name())
	preSelected := mf.Backlog().FilterAndSortOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidTransactionPace(wOut.Timestamp(), a.TargetTs()) {
			return false
		}
		// fast filtering out already consumed outputs in the predecessor milestone context
		return !mf.isConsumedInThePastPath(wOut, a.Extending().VID)
	})
	mf.Tracef(TraceTag, "AttachTagAlongInputs %s. Pre-selected: %d", a.Name(), len(preSelected))

	for _, wOut := range preSelected {
		if success, err := a.InsertTagAlongInput(wOut); success {
			numInserted++
			mf.Tracef(TraceTag, "AttachTagAlongInputs %s. Inserted %s", a.Name(), wOut.IDShortString)
		} else {
			mf.Tracef(TraceTag, "AttachTagAlongInputs %s. Failed to insert %s. Err='%v'", a.Name(), wOut.IDShortString, err)
		}
		if a.NumInputs() >= mf.maxTagAlongInputs {
			break
		}
	}
	return
}

func (mf *MilestoneFactory) startProposerWorkers(targetTime ledger.Time, ctx context.Context) {
	for strategyName, s := range allProposingStrategies {
		task := proposer_generic.New(mf, s, targetTime, ctx)
		if task == nil {
			mf.Tracef(TraceTag, "SKIP '%s' proposer for the target %s", strategyName, targetTime.String)
			continue
		}
		mf.Tracef(TraceTag, "RUN '%s' proposer for the target %s", strategyName, targetTime.String)

		runFun := func() {
			mf.Tracef(TraceTag, " START proposer %s", task.GetName())
			task.Run()
			mf.Tracef(TraceTag, " END proposer %s", task.GetName())
		}
		const debuggerFriendly = false
		if debuggerFriendly {
			runFun()
		} else {
			util.RunWrappedRoutine(mf.Environment.SequencerName()+"::"+task.GetName(), runFun,
				func(err error) bool {
					if errors.Is(err, vertex.ErrDeletedVertexAccessed) {
						// do not panic, just abandon
						mf.Log().Warnf("startProposerWorkers: %v", err)
						return false
					}
					mf.Log().Fatalf("startProposerWorkers: %v\n%s", err, string(debug.Stack()))
					return true
				})
		}
	}
}

func (mf *MilestoneFactory) Propose(a *attacher.IncrementalAttacher) (forceExit bool) {
	coverage := a.LedgerCoverage()
	mf.Tracef(TraceTag, "Propose%s: extend: %s, coverage %s", a.Name(), util.Ref(a.Extending()).IDShortString, coverage.String)

	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	if coverage.Sum() <= mf.proposal.bestSoFarCoverage.Sum() {
		mf.Tracef(TraceTag, "Propose%s: proposal REJECTED due to no increase in coverage (%s vs prev %s)",
			a.Name(), coverage.String(), mf.proposal.bestSoFarCoverage.String)
		return
	}

	commandParser := NewCommandParser(ledger.AddressED25519FromPrivateKey(mf.ControllerPrivateKey()))
	// inflate whenever possible
	tx, err := a.MakeSequencerTransaction(mf.Environment.SequencerName(), mf.ControllerPrivateKey(), commandParser)
	if err != nil {
		mf.Log().Warnf("Propose%s: error during transaction generation: '%v'", a.Name(), err)
		return true
	}

	// now we are taking into account also transaction ID. This is important for equal coverages
	if bestInSlot := mf.BestMilestoneInTheSlot(a.TargetTs().Slot()); bestInSlot != nil {
		if vertex.IsPreferredBase(bestInSlot.LedgerCoverageSum(), coverage.Sum(), &bestInSlot.ID, tx.ID()) {
			mf.Tracef(TraceTag, "Propose%s: proposal REJECTED due to better milestone %s in the same slot %s",
				a.Name(), mf.proposal.bestSoFarCoverage.String, a.TargetTs().Slot())
			return false
		}
	}

	mf.proposal.current = tx
	mf.proposal.bestSoFarCoverage = coverage
	mf.proposal.currentExtended = a.Extending()
	mf.Tracef(TraceTag, "Propose%s: proposal %s ACCEPTED", a.Name(), tx.IDShortString)
	return
}

func (mf *MilestoneFactory) getLatestProposal() *transaction.Transaction {
	mf.proposal.mutex.RLock()
	defer mf.proposal.mutex.RUnlock()

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

const TraceTagChooseExtendEndorsePair = "ChooseExtendEndorsePair"

// ChooseExtendEndorsePair implements one of possible strategies
func (mf *MilestoneFactory) ChooseExtendEndorsePair(proposerName string, targetTs ledger.Time) *attacher.IncrementalAttacher {
	util.Assertf(targetTs.Tick() != 0, "targetTs.Tick() != 0")
	endorseCandidates := mf.Backlog().CandidatesToEndorseSorted(targetTs)
	mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> target %s {%s}", targetTs.String(), vertex.VerticesShortLines(endorseCandidates).Join(", "))

	seqID := mf.SequencerID()
	var ret *attacher.IncrementalAttacher
	for _, endorse := range endorseCandidates {
		if !ledger.ValidTransactionPace(endorse.Timestamp(), targetTs) {
			mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> !ledger.ValidTransactionPace")
			continue
		}
		rdr := multistate.MakeSugared(mf.GetStateReaderForTheBranch(&endorse.BaselineBranch().ID))
		seqOut, err := rdr.GetChainOutput(&seqID)
		if errors.Is(err, multistate.ErrNotFound) {
			mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> GetChainOutput not found")
			continue
		}
		util.AssertNoError(err)
		extendRoot := attacher.AttachOutputID(seqOut.ID, mf)
		futureConeMilestones := mf.futureConeOwnMilestonesOrdered(extendRoot, targetTs)

		mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> check endorsement candidate %s against future cone of extension candidates {%s}",
			endorse.IDShortString, func() string { return vertex.WrappedOutputsShortLines(futureConeMilestones).Join(", ") })

		if ret = mf.chooseEndorseExtendPair(proposerName, targetTs, endorse, futureConeMilestones); ret != nil {
			mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> chooseEndorseExtendPair return %s", ret.Name())
			return ret
		}
	}
	mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> chooseEndorseExtendPair nil")
	return nil
}

func (mf *MilestoneFactory) chooseEndorseExtendPair(proposerName string, targetTs ledger.Time, endorse *vertex.WrappedTx, extendCandidates []vertex.WrappedOutput) *attacher.IncrementalAttacher {
	var ret *attacher.IncrementalAttacher
	for _, extend := range extendCandidates {
		a, err := attacher.NewIncrementalAttacher(proposerName, mf, targetTs, extend, endorse)
		if err != nil {
			mf.Tracef(TraceTagChooseExtendEndorsePair, "%s can't extend %s and endorse %s: %v", targetTs.String, extend.IDShortString, endorse.IDShortString, err)
			continue
		}
		util.Assertf(a != nil, "a != nil")
		if !a.Completed() {
			mf.Tracef(TraceTagChooseExtendEndorsePair, "%s not completed", a.Name())
			continue
		}
		if ret == nil || a.LedgerCoverageSum() > ret.LedgerCoverageSum() {
			ret = a
		}
	}
	return ret
}

func (mf *MilestoneFactory) futureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs ledger.Time) []vertex.WrappedOutput {
	mf.cleanOwnMilestonesIfNecessary()

	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	mf.Tracef(TraceTag, "futureConeOwnMilestonesOrdered for root output %s. Total %d own milestones", rootOutput.IDShortString, len(mf.ownMilestones))

	_, ok := mf.ownMilestones[rootOutput.VID]
	util.Assertf(ok, "futureConeOwnMilestonesOrdered: milestone output %s of chain %s is expected to be among set of own milestones (%d)",
		rootOutput.IDShortString, util.Ref(mf.SequencerID()).StringShort, len(mf.ownMilestones))

	ordered := util.SortKeys(mf.ownMilestones, func(vid1, vid2 *vertex.WrappedTx) bool {
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

func (mf *MilestoneFactory) BestMilestoneInTheSlot(slot ledger.Slot) *vertex.WrappedTx {
	all := mf.LatestMilestonesDescending(func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool {
		return vid.Slot() == slot
	})
	if len(all) == 0 {
		return nil
	}
	return all[0]
}
