package factory

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_base"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_endorse1"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
	"github.com/lunfardo314/proxima/sequencer/tippool"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

type (
	Environment interface {
		attacher.Environment
		tippool.Environment
		ControllerPrivateKey() ed25519.PrivateKey
		SequencerName() string
		Context() context.Context
	}

	MilestoneFactory struct {
		Environment
		mutex                       sync.RWMutex
		tipPool                     *tippool.SequencerTipPool
		proposal                    latestMilestoneProposal
		ownMilestones               map[*vertex.WrappedTx]set.Set[vertex.WrappedOutput] // map ms -> consumed outputs in the past
		maxTagAlongInputs           int
		lastPruned                  time.Time
		ownMilestoneCount           int
		removedMilestonesSinceReset int
		// past combinations
		pastCombinationsMutex     sync.RWMutex
		pastCombinations          map[[32]byte]time.Time
		pastCombinationsNextPurge time.Time
	}

	latestMilestoneProposal struct {
		mutex             sync.RWMutex
		targetTs          ledger.LogicalTime
		bestSoFarCoverage uint64
		current           *transaction.Transaction
		currentExtended   vertex.WrappedOutput
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
	registerProposerStrategy(proposer_endorse1.Strategy())
}

const (
	maxAdditionalOutputs    = 256 - 2 // 1 for chain output, 1 for stem
	veryMaxTagAlongInputs   = maxAdditionalOutputs
	cleanupMilestonesPeriod = 1 * time.Second
)

func New(env Environment, maxTagAlongInputs int) (*MilestoneFactory, error) {
	ret := &MilestoneFactory{
		Environment:       env,
		proposal:          latestMilestoneProposal{},
		ownMilestones:     make(map[*vertex.WrappedTx]set.Set[vertex.WrappedOutput]),
		maxTagAlongInputs: maxTagAlongInputs,
	}
	if ret.maxTagAlongInputs == 0 || ret.maxTagAlongInputs > veryMaxTagAlongInputs {
		ret.maxTagAlongInputs = veryMaxTagAlongInputs
	}
	var err error
	ret.tipPool, err = tippool.New(ret, env.SequencerName())
	if err != nil {
		return nil, err
	}
	env.Log().Debugf("milestone factory created")
	return ret, nil
}

func (mf *MilestoneFactory) isConsumedInThePastPath(wOut vertex.WrappedOutput, ms *vertex.WrappedTx) bool {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	return mf.ownMilestones[ms].Contains(wOut)
}

func (mf *MilestoneFactory) OwnLatestMilestone() *vertex.WrappedTx {
	latest := mf.tipPool.GetOwnLatestMilestoneTx()
	if latest != nil {
		mf.AddOwnMilestone(latest)
	}
	return latest
}

func (mf *MilestoneFactory) AddOwnMilestone(vid *vertex.WrappedTx) {
	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	if _, already := mf.ownMilestones[vid]; already {
		return
	}

	consumed := set.New[vertex.WrappedOutput](vid.WrappedInputs()...)
	if vid.IsSequencerMilestone() {
		if prev := vid.SequencerPredecessor(); prev != nil {
			if prevConsumed, found := mf.ownMilestones[prev]; found {
				consumed.AddAll(prevConsumed)
			}
		}
	}
	mf.ownMilestones[vid] = consumed
	mf.ownMilestoneCount++
}

func (mf *MilestoneFactory) StartProposingForTargetLogicalTime(targetTs ledger.LogicalTime) *transaction.Transaction {
	deadline := targetTs.Time()
	nowis := time.Now()
	mf.Tracef("seq", "StartProposingForTargetLogicalTime: targetTs: %v, nowis: %v", deadline, nowis)

	if deadline.Before(nowis) {
		mf.Tracef("seq", "deadline is in the past: impossible to generate milestone")
		return nil
	}
	// start worker(s)
	mf.setNewTarget(targetTs)
	ctx, cancel := context.WithDeadline(mf.Context(), deadline)
	defer cancel() // to prevent context leak
	mf.startProposerWorkers(targetTs, ctx)

	<-ctx.Done()

	ret := mf.getLatestProposal() // will return nil if wasn't able to generate transaction
	return ret
}

// setNewTarget signals proposer allMilestoneProposingStrategies about new timestamp,
// Returns last proposed proposal
func (mf *MilestoneFactory) setNewTarget(ts ledger.LogicalTime) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	if mf.proposal.targetTs.IsSlotBoundary() || mf.proposal.targetTs.Slot() != ts.Slot() {
		// if starting new slot, reset best coverage delta
		mf.proposal.bestSoFarCoverage = 0
	}
	mf.proposal.targetTs = ts
	mf.proposal.current = nil
}

func (mf *MilestoneFactory) CurrentTargetTs() ledger.LogicalTime {
	mf.proposal.mutex.RLock()
	defer mf.proposal.mutex.RUnlock()

	return mf.proposal.targetTs
}

func (mf *MilestoneFactory) AttachTagAlongInputs(a *attacher.IncrementalAttacher) (numInserted int) {
	mf.Tracef("seq", "AttachTagAlongInputs: %s", a.Name())
	preSelected := mf.tipPool.FilterAndSortOutputs(func(wOut vertex.WrappedOutput) bool {
		if !ledger.ValidTimePace(wOut.Timestamp(), a.TargetTs()) {
			return false
		}
		// fast filtering out already consumed outputs in the predecessor milestone context
		return !mf.isConsumedInThePastPath(wOut, a.Extending())
	})
	mf.Tracef("seq", "AttachTagAlongInputs %s. Pre-selected: %d", a.Name(), len(preSelected))

	for _, wOut := range preSelected {
		if a.InsertTagAlongInput(wOut, set.New[*vertex.WrappedTx]()) {
			numInserted++
		}
		if a.NumInputs() >= mf.maxTagAlongInputs {
			break
		}
	}
	return
}

func (mf *MilestoneFactory) startProposerWorkers(targetTime ledger.LogicalTime, ctx context.Context) {
	for strategyName, s := range allProposingStrategies {
		task := proposer_generic.New(mf, s, targetTime, ctx)
		if task == nil {
			mf.Tracef("proposer", "SKIP '%s' proposer for the target %s", strategyName, targetTime.String)
			continue
		}
		mf.Tracef("proposer", "RUN '%s' proposer for the target %s", strategyName, targetTime.String)

		util.RunWrappedRoutine(mf.SequencerName()+"::"+task.GetName(), func() {
			mf.Tracef("proposer", " START proposer %s", task.GetName())
			task.Run()
			mf.Tracef("proposer", " END proposer %s", task.GetName())
		}, func(err error) {
			mf.Log().Fatal(err)
		}, common.ErrDBUnavailable, vertex.ErrDeletedVertexAccessed)
	}
}

func (mf *MilestoneFactory) Propose(a *attacher.IncrementalAttacher) (forceExit bool) {
	coverage := a.LedgerCoverageSum()
	mf.Tracef("proposer", "factory.Propose%s: coverage %s", a.Name(), util.GoThousandsLazy(coverage))

	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	if coverage <= mf.proposal.bestSoFarCoverage {
		mf.Tracef("proposer", "factory.Propose%s proposal REJECTED due to no increase in coverage (%s vs prev %s)",
			a.Name(), util.GoThousands(coverage), util.GoThousands(mf.proposal.bestSoFarCoverage))
		return
	}

	tx, err := a.MakeTransaction(mf.SequencerName(), mf.ControllerPrivateKey())
	if err != nil {
		mf.Log().Warnf("factory.Propose%s: error during transaction generation: '%v'", a.Name(), err)
		return true
	}

	mf.proposal.current = tx
	mf.proposal.bestSoFarCoverage = coverage
	mf.proposal.currentExtended = a.ExtendedOutput()
	mf.Tracef("proposer", "factory.Propose%s proposal %s ACCEPTED", a.Name(), tx.IDShortString)
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

// ChooseExtendEndorsePair implements one of possible strategies
func (mf *MilestoneFactory) ChooseExtendEndorsePair(proposerName string, targetTs ledger.LogicalTime) *attacher.IncrementalAttacher {
	endorseCandidates := mf.tipPool.OtherMilestonesSorted()
	seqID := mf.SequencerID()
	var ret *attacher.IncrementalAttacher
	for _, endorse := range endorseCandidates {
		if !ledger.ValidTimePace(endorse.Timestamp(), targetTs) {
			continue
		}
		rdr := multistate.MakeSugared(mf.GetStateReaderForTheBranch(endorse.BaselineBranch()))
		seqOut, err := rdr.GetChainOutput(&seqID)
		if errors.Is(err, multistate.ErrNotFound) {
			continue
		}
		util.AssertNoError(err)
		extendRoot := attacher.AttachTxID(seqOut.ID.TransactionID(), mf)
		futureConeMilestones := mf.futureConeMilestonesOrdered(extendRoot, targetTs)
		ret = mf.chooseEndorseExtendPair(proposerName, targetTs, endorse, futureConeMilestones)
		if ret != nil {
			// return first suitable pair. The search is not exhaustive along all possible endorsements
			mf.rememberExtendEndorseCombination(ret.Extending(), endorse)
			return ret
		}
	}
	return nil
}

func (mf *MilestoneFactory) chooseEndorseExtendPair(proposerName string, targetTs ledger.LogicalTime, endorse *vertex.WrappedTx, extendCandidates []*vertex.WrappedTx) *attacher.IncrementalAttacher {
	var ret *attacher.IncrementalAttacher
	for _, extend := range extendCandidates {
		if mf.knownExtendEndorseCombination(extend, endorse) {
			continue
		}
		a, err := attacher.NewIncrementalAttacher(proposerName, mf, targetTs, extend, endorse)
		if err != nil {
			mf.Tracef("proposer", "can't extend %s and endorse %s: %v", extend.IDShortString, endorse.IDShortString, err)
			continue
		}
		util.Assertf(a != nil, "a != nil")
		if !a.Completed() {
			continue
		}
		if ret == nil || a.LedgerCoverageSum() > ret.LedgerCoverageSum() {
			ret = a
		}
	}
	return ret
}

func (mf *MilestoneFactory) futureConeMilestonesOrdered(rootVID *vertex.WrappedTx, targetTs ledger.LogicalTime) []*vertex.WrappedTx {
	mf.cleanOwnMilestonesIfNecessary()

	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	//p.setTraceNAhead(1)
	mf.Tracef("proposer", "futureConeMilestonesOrdered for root %s. Total %d own milestones", rootVID.IDShortString, len(mf.ownMilestones))

	_, ok := mf.ownMilestones[rootVID]
	util.Assertf(ok, "futureConeMilestonesOrdered: milestone %s of chain %s is expected to be among set of own milestones (%d)",
		rootVID.IDShortString, util.Ref(mf.SequencerID()).StringShort, len(mf.ownMilestones))

	ordered := util.SortKeys(mf.ownMilestones, func(vid1, vid2 *vertex.WrappedTx) bool {
		// by timestamp -> equivalent to topological order, ascending, i.e. older first
		return vid1.Timestamp().Before(vid2.Timestamp())
	})

	visited := set.New[*vertex.WrappedTx](rootVID)
	ret := []*vertex.WrappedTx{rootVID}
	for _, vid := range ordered {
		if !vid.IsBadOrDeleted() &&
			vid.IsSequencerMilestone() &&
			visited.Contains(vid.SequencerPredecessor()) &&
			ledger.ValidTimePace(vid.Timestamp(), targetTs) {
			visited.Insert(vid)
			ret = append(ret, vid)
		}
	}
	return ret
}

func (mf *MilestoneFactory) NumOutputsInBuffer() int {
	return mf.tipPool.NumOutputsInBuffer()
}

func (mf *MilestoneFactory) NumMilestones() int {
	return mf.tipPool.NumMilestones()
}

func extendEndorseCombinationHash(extend *vertex.WrappedTx, endorse ...*vertex.WrappedTx) (ret [32]byte) {
	if len(endorse) == 0 {
		ret = extend.ID
		return
	}
	var buf bytes.Buffer
	buf.Write(extend.ID[:])
	for _, vid := range endorse {
		buf.Write(vid.ID[:])
	}
	return blake2b.Sum256(buf.Bytes())
}

const (
	pastCombinationTTL = time.Minute
	purgePeriod        = 10 * time.Second
)

func (mf *MilestoneFactory) _purgePastCombinations() {
	nowis := time.Now()
	if nowis.Before(mf.pastCombinationsNextPurge) {
		return
	}
	toDelete := make([][32]byte, 0)
	for h, deadline := range mf.pastCombinations {
		if deadline.Before(nowis) {
			toDelete = append(toDelete, h)
		}
	}
	for i := range toDelete {
		delete(mf.pastCombinations, toDelete[i])
	}
	mf.pastCombinationsNextPurge = nowis.Add(purgePeriod)
}

func (mf *MilestoneFactory) knownExtendEndorseCombination(extend *vertex.WrappedTx, endorse ...*vertex.WrappedTx) bool {
	h := extendEndorseCombinationHash(extend, endorse...)

	mf.pastCombinationsMutex.RLock()
	defer mf.pastCombinationsMutex.RUnlock()

	_, already := mf.pastCombinations[h]
	return already
}

func (mf *MilestoneFactory) rememberExtendEndorseCombination(extend *vertex.WrappedTx, endorse ...*vertex.WrappedTx) {
	mf.pastCombinationsMutex.Lock()
	defer mf.pastCombinationsMutex.Unlock()

	h := extendEndorseCombinationHash(extend, endorse...)
	mf.pastCombinations[h] = time.Now().Add(pastCombinationTTL)
	mf._purgePastCombinations()
}
