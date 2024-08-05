package factory

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/sequencer/factory/commands"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_base"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_endorse1"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_endorse2"
	"github.com/lunfardo314/proxima/sequencer/factory/proposer_generic"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/viper"
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
		target                      target
		maxTagAlongInputs           int
		ownMilestoneCount           int
		removedMilestonesSinceReset int
	}

	outputsWithTime struct {
		consumed set.Set[vertex.WrappedOutput]
		since    time.Time
	}

	target struct {
		mutex     sync.RWMutex
		targetTs  ledger.Time
		proposals []proposal
	}

	proposal struct {
		tx           *transaction.Transaction
		txMetadata   *txmetadata.TransactionMetadata
		extended     vertex.WrappedOutput
		coverage     uint64
		attacherName string
	}

	Stats struct {
		NumOwnMilestones            int
		OwnMilestoneCount           int
		RemovedMilestonesSinceReset int
		backlog.Stats
	}
)

var _allProposingStrategies = make(map[string]*proposer_generic.Strategy)

func registerProposerStrategy(s *proposer_generic.Strategy) {
	_allProposingStrategies[s.Name] = s
}

func init() {
	registerProposerStrategy(proposer_base.Strategy())
	registerProposerStrategy(proposer_endorse1.Strategy())
	registerProposerStrategy(proposer_endorse2.Strategy())
}

func allProposingStrategies() []*proposer_generic.Strategy {
	ret := make([]*proposer_generic.Strategy, 0)
	for _, s := range _allProposingStrategies {
		if !viper.GetBool("strategies.disable_proposer." + s.ShortName) {
			ret = append(ret, s)
		}
	}
	return ret
}

const (
	maxAdditionalOutputs  = 256 - 2 // 1 for chain output, 1 for stem
	veryMaxTagAlongInputs = maxAdditionalOutputs
	TraceTag              = "factory"
	TraceTagMining        = "mining"
)

func New(env Environment) (*MilestoneFactory, error) {
	ret := &MilestoneFactory{
		Environment: env,
		target: target{
			proposals: make([]proposal, 0),
		},
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

func (mf *MilestoneFactory) StartProposingForTargetLogicalTime(targetTs ledger.Time) (*transaction.Transaction, *txmetadata.TransactionMetadata) {
	deadline := targetTs.Time()
	nowis := time.Now()
	mf.Tracef(TraceTag, "StartProposingForTargetLogicalTime: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))

	if deadline.Before(nowis) {
		mf.Tracef(TraceTag, "target %s is in the past by %v: impossible to generate milestone",
			targetTs.String, nowis.Sub(deadline))
		return nil, nil
	}
	// start worker(s)
	mf.setNewTarget(targetTs)
	ctx, cancel := context.WithDeadline(mf.Ctx(), deadline)
	defer cancel() // to prevent context leak
	mf.startProposerWorkers(targetTs, ctx)

	<-ctx.Done()

	return mf.getBestProposal() // will return nil if wasn't able to generate transaction
}

// setNewTarget sets new target for proposers
func (mf *MilestoneFactory) setNewTarget(ts ledger.Time) {
	mf.target.mutex.Lock()
	defer mf.target.mutex.Unlock()

	mf.target.targetTs = ts
	mf.target.proposals = util.ClearSlice(mf.target.proposals)
}

func (mf *MilestoneFactory) CurrentTargetTs() ledger.Time {
	mf.target.mutex.RLock()
	defer mf.target.mutex.RUnlock()

	return mf.target.targetTs
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

func (mf *MilestoneFactory) startProposerWorkers(targetTime ledger.Time, ctx context.Context) {
	for _, s := range allProposingStrategies() {
		task := proposer_generic.New(mf, s, targetTime, ctx)
		if task == nil {
			mf.Tracef(TraceTag, "SKIP '%s' proposer for the target %s", s.Name, targetTime.String)
			continue
		}
		mf.Tracef(TraceTag, "RUN '%s' proposer for the target %s", s.Name, targetTime.String)

		runFun := func() {
			mf.Tracef(TraceTag, " START proposer %s", task.GetName())

			mf.MarkWorkProcessStarted(task.GetName())
			task.Run()
			mf.MarkWorkProcessStopped(task.GetName())

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
					mf.Log().Fatalf("startProposerWorkers: %v", err)
					return true
				})
		}
	}
}

func (mf *MilestoneFactory) makeTxProposal(a *attacher.IncrementalAttacher, strategyName string) (*transaction.Transaction, error) {
	cmdParser := commands.NewCommandParser(ledger.AddressED25519FromPrivateKey(mf.ControllerPrivateKey()))
	nm := mf.Environment.SequencerName()
	if strategyName != "" {
		nm += "." + strategyName
	}
	tx, err := a.MakeSequencerTransaction(nm, mf.ControllerPrivateKey(), cmdParser)
	// attacher and references not needed anymore, should be released
	a.Close()
	return tx, err
}

func (mf *MilestoneFactory) Propose(a *attacher.IncrementalAttacher, strategyName string) error {
	if a.TargetTs().Tick() == 0 {
		return mf.proposeBranchTx(a, strategyName)
	}
	return mf.proposeNonBranchTx(a, strategyName)
}

func (mf *MilestoneFactory) proposeNonBranchTx(a *attacher.IncrementalAttacher, strategyName string) error {
	util.Assertf(a.TargetTs().Tick() != 0, "must be non-branch tx")

	tx, err := mf.makeTxProposal(a, strategyName)
	util.Assertf(a.IsClosed(), "a.IsDisposed()")

	if err != nil {
		return err
	}

	coverage := a.LedgerCoverage()
	mf.addProposal(proposal{
		tx: tx,
		txMetadata: &txmetadata.TransactionMetadata{
			StateRoot:               nil,
			LedgerCoverage:          util.Ref(coverage),
			SlotInflation:           util.Ref(a.SlotInflation()),
			IsResponseToPull:        false,
			SourceTypeNonPersistent: txmetadata.SourceTypeSequencer,
		},
		extended:     a.Extending(),
		coverage:     coverage,
		attacherName: a.Name(),
	})
	return nil
}

func (mf *MilestoneFactory) proposeBranchTx(a *attacher.IncrementalAttacher, strategyName string) error {
	util.Assertf(a.TargetTs().Tick() == 0, "must be branch tx")

	tx, err := mf.makeTxProposal(a, strategyName)
	util.Assertf(a.IsClosed(), "a.IsDisposed()")

	if err != nil {
		return err
	}
	coverage := a.LedgerCoverage()
	mf.addProposal(proposal{
		tx: tx,
		txMetadata: &txmetadata.TransactionMetadata{
			StateRoot:               nil,
			LedgerCoverage:          util.Ref(coverage),
			SlotInflation:           util.Ref(a.SlotInflation()),
			IsResponseToPull:        false,
			SourceTypeNonPersistent: txmetadata.SourceTypeSequencer,
		},
		extended:     a.Extending(),
		coverage:     coverage,
		attacherName: a.Name(),
	})
	return nil
}

func (mf *MilestoneFactory) addProposal(p proposal) {
	mf.target.mutex.Lock()
	defer mf.target.mutex.Unlock()

	mf.target.proposals = append(mf.target.proposals, p)
	mf.Tracef(TraceTag, "added proposal %s from %s: coverage %s",
		p.tx.IDShortString, p.attacherName, func() string { return util.Th(p.coverage) })
}

func (mf *MilestoneFactory) getBestProposal() (*transaction.Transaction, *txmetadata.TransactionMetadata) {
	mf.target.mutex.RLock()
	defer mf.target.mutex.RUnlock()

	bestCoverageInSlot := mf.BestCoverageInTheSlot(mf.target.targetTs)
	mf.Tracef(TraceTag, "best coverage in slot: %s", func() string { return util.Th(bestCoverageInSlot) })
	maxIdx := -1
	for i := range mf.target.proposals {
		c := mf.target.proposals[i].coverage
		if c > bestCoverageInSlot {
			bestCoverageInSlot = c
			maxIdx = i
		}
	}
	if maxIdx < 0 {
		mf.Tracef(TraceTag, "getBestProposal: NONE, target: %s", mf.target.targetTs.String)
		return nil, nil
	}
	p := mf.target.proposals[maxIdx]
	mf.Tracef(TraceTag, "getBestProposal: %s, target: %s, attacher %s: coverage %s",
		p.tx.IDShortString, mf.target.targetTs.String, p.attacherName, func() string { return util.Th(p.coverage) })
	return p.tx, p.txMetadata
}

const TraceTagChooseExtendEndorsePair = "ChooseExtendEndorsePair"

// ChooseExtendEndorsePair implements one of possible strategies:
// finds a pair: milestone to extend and another sequencer transaction to endorse, as heavy as possible
func (mf *MilestoneFactory) ChooseExtendEndorsePair(proposerName string, targetTs ledger.Time) *attacher.IncrementalAttacher {
	mf.Assertf(targetTs.Tick() != 0, "targetTs.Tick() != 0")
	endorseCandidates := mf.Backlog().CandidatesToEndorseSorted(targetTs)
	mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> target %s {%s}",
		targetTs.String, vertex.VerticesShortLines(endorseCandidates).Join(", "))

	seqID := mf.SequencerID()
	var ret *attacher.IncrementalAttacher
	for _, endorse := range endorseCandidates {
		if !ledger.ValidTransactionPace(endorse.Timestamp(), targetTs) {
			// cannot endorse candidate because of ledger time constraint
			mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> !ledger.ValidTransactionPace")
			continue
		}
		rdr := multistate.MakeSugared(mf.GetStateReaderForTheBranch(&endorse.BaselineBranch().ID))
		seqOut, err := rdr.GetChainOutput(&seqID)
		if errors.Is(err, multistate.ErrNotFound) {
			mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> GetChainOutput not found")
			continue
		}
		mf.AssertNoError(err)
		extendRoot := attacher.AttachOutputID(seqOut.ID, mf)

		mf.AddOwnMilestone(extendRoot.VID) // to ensure it is in the pool of own milestones
		futureConeMilestones := mf.futureConeOwnMilestonesOrdered(extendRoot, targetTs)

		mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> check endorsement candidate %s against future cone of extension candidates {%s}",
			endorse.IDShortString, func() string { return vertex.WrappedOutputsShortLines(futureConeMilestones).Join(", ") })

		if ret = mf.chooseEndorseExtendPairAttacher(proposerName, targetTs, endorse, futureConeMilestones); ret != nil {
			mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> chooseEndorseExtendPairAttacher return %s", ret.Name())
			return ret
		}
	}
	mf.Tracef(TraceTagChooseExtendEndorsePair, ">>>>>>>>>>>>>>> chooseEndorseExtendPairAttacher nil")
	return nil
}

// chooseEndorseExtendPairAttacher traverses all known extension options and check each of it with the endorsement target
// Returns consistent incremental attacher with the biggest ledger coverage
func (mf *MilestoneFactory) chooseEndorseExtendPairAttacher(proposerName string, targetTs ledger.Time, endorse *vertex.WrappedTx, extendCandidates []vertex.WrappedOutput) *attacher.IncrementalAttacher {
	var ret, a *attacher.IncrementalAttacher
	var err error
	for _, extend := range extendCandidates {
		a, err = attacher.NewIncrementalAttacher(proposerName, mf, targetTs, extend, endorse)
		if err != nil {
			mf.Tracef(TraceTagChooseExtendEndorsePair, "%s can't extend %s and endorse %s: %v", targetTs.String, extend.IDShortString, endorse.IDShortString, err)
			continue
		}
		// we must carefully dispose unused references, otherwise pruning does not work
		// we dispose all attachers with their references, except the one with the biggest coverage
		switch {
		case !a.Completed():
			a.Close()
		case ret == nil:
			ret = a
		case a.LedgerCoverage() > ret.LedgerCoverage():
			ret.Close()
			ret = a
		default:
			a.Close()
		}
	}
	return ret
}

func (mf *MilestoneFactory) futureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs ledger.Time) []vertex.WrappedOutput {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	mf.Tracef(TraceTag, "futureConeOwnMilestonesOrdered for root output %s. Total %d own milestones", rootOutput.IDShortString, len(mf.ownMilestones))

	_, ok := mf.ownMilestones[rootOutput.VID]
	mf.Assertf(ok, "futureConeOwnMilestonesOrdered: milestone output %s of chain %s is expected to be among set of own milestones (%d)",
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
