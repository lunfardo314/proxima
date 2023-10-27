package sequencer

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	milestoneFactory struct {
		mutex                       sync.RWMutex
		seqName                     string
		log                         *zap.SugaredLogger
		tangle                      *utangle.UTXOTangle
		tipPool                     *sequencerTipPool
		controllerKey               ed25519.PrivateKey
		proposal                    latestMilestoneProposal
		ownMilestones               map[*utangle.WrappedTx]ownMilestone
		maxFeeInputs                int
		lastPruned                  time.Time
		ownMilestoneCount           int
		removedMilestonesSinceReset int
	}

	ownMilestone struct {
		utangle.WrappedOutput
		consumedInThePastPath set.Set[utangle.WrappedOutput]
	}

	proposedMilestoneWithData struct {
		tx         *transaction.Transaction
		coverage   uint64
		elapsed    time.Duration
		proposedBy string
	}

	latestMilestoneProposal struct {
		mutex             sync.RWMutex
		targetTs          core.LogicalTime
		bestSoFarCoverage uint64
		current           *transaction.Transaction
		durations         []time.Duration
	}

	factoryStats struct {
		numOwnMilestones            int
		ownMilestoneCount           int
		removedMilestonesSinceReset int
		tipPoolStats
	}
)

const (
	maxAdditionalOutputs = 256 - 2              // 1 for chain output, 1 for stem
	veryMaxFeeInputs     = maxAdditionalOutputs // edge case with sequencer commands
)

func (seq *Sequencer) createMilestoneFactory() error {
	logname := fmt.Sprintf("[%sF-%s]", seq.config.SequencerName, seq.chainID.VeryShort())
	log := general.NewLogger(logname, seq.config.LogLevel, seq.config.LogOutputs, seq.config.LogTimeLayout)

	chainOut := seq.config.StartOutput
	if chainOut.VID == nil {
		rdr := seq.glb.UTXOTangle().HeaviestStateForLatestTimeSlot()
		odata, err := rdr.GetUTXOForChainID(&seq.chainID)
		if err != nil {
			return fmt.Errorf("can't get chain output: %v", err)
		}
		var hasIt, invalid bool
		chainOut, hasIt, invalid = seq.glb.UTXOTangle().GetWrappedOutput(&odata.ID, rdr)
		util.Assertf(hasIt && !invalid, "can't retrieve chain output")
	}
	var err error

	ownMilestones := map[*utangle.WrappedTx]ownMilestone{
		chainOut.VID: newOwnMilestone(chainOut),
	}

	tippoolLoglevel := seq.config.LogLevel
	if seq.config.TraceTippool {
		tippoolLoglevel = zapcore.DebugLevel
	}
	tippool, err := startTipPool(seq.config.SequencerName, seq.glb, seq.chainID, tippoolLoglevel)
	if err != nil {
		return err
	}

	ret := &milestoneFactory{
		seqName:       seq.config.SequencerName,
		log:           log,
		tangle:        seq.glb.UTXOTangle(),
		tipPool:       tippool,
		ownMilestones: ownMilestones,
		controllerKey: seq.controllerKey,
		maxFeeInputs:  seq.config.MaxFeeInputs,
	}
	if ret.maxFeeInputs == 0 || ret.maxFeeInputs > veryMaxFeeInputs {
		ret.maxFeeInputs = veryMaxFeeInputs
	}
	ret.log.Debugf("milestone factory started")

	seq.factory = ret
	return nil
}

func (mf *milestoneFactory) trace(format string, args ...any) {
	if traceAll.Load() {
		mf.log.Infof("TRACE "+format, args...)
	}
}

func newOwnMilestone(wOut utangle.WrappedOutput, inputs ...utangle.WrappedOutput) ownMilestone {
	return ownMilestone{
		WrappedOutput:         wOut,
		consumedInThePastPath: set.New[utangle.WrappedOutput](inputs...),
	}
}

func (mf *milestoneFactory) makeMilestone(chainIn, stemIn *utangle.WrappedOutput, preSelectedFeeInputs []utangle.WrappedOutput, endorse []*utangle.WrappedTx, targetTs core.LogicalTime) (*transaction.Transaction, error) {
	chainInReal, err := chainIn.Unwrap()
	if err != nil || chainInReal == nil {
		return nil, err
	}
	var stemInReal *core.OutputWithID
	if stemIn != nil {
		stemInReal, err = stemIn.Unwrap()
		if err != nil || stemInReal == nil {
			return nil, err
		}
	}
	feeInputsReal := make([]*core.OutputWithID, len(preSelectedFeeInputs))
	for i, wOut := range preSelectedFeeInputs {
		feeInputsReal[i], err = wOut.Unwrap()
		if err != nil {
			return nil, err
		}
		if feeInputsReal[i] == nil {
			return nil, nil
		}
	}
	// interpret sequencer commands contained in fee consumedInThePastPath. This also possibly adjusts consumedInThePastPath
	var additionalOutputs []*core.Output
	capWithdrawals := uint64(0)
	if chainInReal.Output.Amount() > core.MinimumAmountOnSequencer {
		capWithdrawals = chainInReal.Output.Amount() - core.MinimumAmountOnSequencer
	}
	feeInputsReal, additionalOutputs = mf.makeAdditionalInputsOutputs(feeInputsReal, capWithdrawals)

	endorseReal := utangle.DecodeIDs(endorse...)

	if err != nil {
		return nil, err
	}
	txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
		SeqName: mf.seqName,
		ChainInput: &core.OutputWithChainID{
			OutputWithID: *chainInReal,
			ChainID:      mf.tipPool.ChainID(),
		},
		StemInput:         stemInReal,
		Timestamp:         targetTs,
		AdditionalInputs:  feeInputsReal,
		AdditionalOutputs: additionalOutputs,
		Endorsements:      endorseReal,
		PrivateKey:        mf.controllerKey,
	})
	if err != nil {
		return nil, err
	}
	return transaction.FromBytesMainChecksWithOpt(txBytes)
}

func (mf *milestoneFactory) addOwnMilestone(wOut utangle.WrappedOutput) {
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
	mf.ownMilestones[wOut.VID] = om
	mf.ownMilestoneCount++
}

func (mf *milestoneFactory) isConsumedInThePastPath(wOut utangle.WrappedOutput, ms *utangle.WrappedTx) bool {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	return mf.ownMilestones[ms].consumedInThePastPath.Contains(wOut)
}

func (mf *milestoneFactory) selectInputs(targetTs core.LogicalTime, ownMs utangle.WrappedOutput, otherSeqVIDs ...*utangle.WrappedTx) ([]utangle.WrappedOutput, *utangle.WrappedOutput) {
	if ownMs.IsConsumed(otherSeqVIDs...) {
		return nil, &ownMs
	}

	allSeqVIDs := append(util.CloneArglistShallow(otherSeqVIDs...), ownMs.VID)

	consolidatedPastTrack, conflict := utangle.MergePastTracks(allSeqVIDs...)
	if conflict != nil {
		return nil, conflict
	}

	// pre-selects not orphaned and with suitable timestamp outputs, sorts by timestamp ascending
	selected := mf.tipPool.filterAndSortOutputs(func(wOut utangle.WrappedOutput) bool {
		if !core.ValidTimePace(wOut.Timestamp(), targetTs) {
			return false
		}
		if mf.isConsumedInThePastPath(wOut, ownMs.VID) {
			// fast filtering out already consumed outputs
			return false
		}
		return true
	})

	// filters outputs which can be merged into the target delta but no more than maxFeeInputs limit
	selected = util.FilterSlice(selected, func(wOut utangle.WrappedOutput) bool {
		if conflict = consolidatedPastTrack.AbsorbVIDSafe(wOut.VID); conflict != nil {
			return false
		}
		return !wOut.IsConsumed(otherSeqVIDs...)
	}, mf.maxFeeInputs)

	return selected, nil
}

func (mf *milestoneFactory) getLatestMilestone() (ret utangle.WrappedOutput) {
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

// setNewTarget signals proposer allMilestoneProposingStrategies about new timestamp,
// Returns last proposed proposal
func (mf *milestoneFactory) setNewTarget(ts core.LogicalTime) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	mf.proposal.targetTs = ts
	mf.proposal.current = nil
	if ts.IsSlotBoundary() {
		mf.proposal.bestSoFarCoverage = 0
	}
	mf.proposal.durations = make([]time.Duration, 0)
}

func (mf *milestoneFactory) storeProposalDuration(d time.Duration) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	mf.proposal.durations = append(mf.proposal.durations, d)
}

func (mf *milestoneFactory) averageProposalDuration() (time.Duration, int) {
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

// continueCandidateProposing the proposing strategy checks if its assumed target timestamp
// is still actual. Strategy keeps proposing latestMilestone candidates until it is no longer actual
func (mc *latestMilestoneProposal) continueCandidateProposing(ts core.LogicalTime) bool {
	mc.mutex.RLock()
	defer mc.mutex.RUnlock()

	return mc.targetTs == ts
}

func (mc *latestMilestoneProposal) getLatestProposal() *transaction.Transaction {
	mc.mutex.RLock()
	defer mc.mutex.RUnlock()

	return mc.current
}

func (mf *milestoneFactory) startProposingForTargetLogicalTime(targetTs core.LogicalTime) (*transaction.Transaction, time.Duration, int) {
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

	ret := mf.proposal.getLatestProposal() // will return nil if wasn't able to generate transaction
	// set target time to nil -> signal workers to exit
	avgProposalDuration, numProposals := mf.averageProposalDuration()
	mf.setNewTarget(core.NilLogicalTime)
	return ret, avgProposalDuration, numProposals
}

func (mf *milestoneFactory) startProposerWorkers(targetTime core.LogicalTime) {

	for strategyName, rec := range allProposingStrategies {
		task := rec.constructor(mf, targetTime)
		if task != nil {
			go mf.runProposerTask(task)
		} else {
			mf.trace("SKIP '%s' proposer for the target time %s", strategyName, targetTime.String())
		}
	}
}

func (mf *milestoneFactory) runProposerTask(task proposerTask) {
	task.trace(" START proposer %s", task.name())
	task.run()
	task.trace(" END proposer %s", task.name())
}

const cleanupMilestonesPeriod = 1 * time.Second

func (mf *milestoneFactory) cleanOwnMilestonesIfNecessary() {
	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	if time.Since(mf.lastPruned) < cleanupMilestonesPeriod {
		return
	}

	toDelete := make([]*utangle.WrappedTx, 0)
	for vid := range mf.ownMilestones {
		vid.Unwrap(utangle.UnwrapOptions{Orphaned: func() {
			toDelete = append(toDelete, vid)
		}})
	}
	for _, vid := range toDelete {
		delete(mf.ownMilestones, vid)
	}
	mf.removedMilestonesSinceReset += len(toDelete)
}

func (mf *milestoneFactory) futureConeMilestonesOrdered(rootVID *utangle.WrappedTx, p proposerTask) []utangle.WrappedOutput {
	mf.cleanOwnMilestonesIfNecessary()

	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	//p.setTraceNAhead(1)
	p.trace("futureConeMilestonesOrdered for root %s. Total %d own milestones", rootVID.LazyIDShort(), len(mf.ownMilestones))

	om, ok := mf.ownMilestones[rootVID]
	util.Assertf(ok, "futureConeMilestonesOrdered: milestone %s of chain %s is expected to be among set of own milestones (%d)",
		rootVID.LazyIDShort(),
		func() any { return mf.tipPool.chainID.Short() },
		len(mf.ownMilestones))

	rootOut := om.WrappedOutput
	ordered := util.SortKeys(mf.ownMilestones, func(vid1, vid2 *utangle.WrappedTx) bool {
		// by timestamp -> equivalent to topological order, ascending, i.e. older first
		return vid1.Timestamp().Before(vid2.Timestamp())
	})

	visited := set.New[*utangle.WrappedTx](rootVID)
	ret := append(make([]utangle.WrappedOutput, 0, len(ordered)), rootOut)
	for _, vid := range ordered {
		if !vid.IsOrphaned() && vid.IsSequencerMilestone() && visited.Contains(vid.SequencerPredecessor()) {
			visited.Insert(vid)
			ret = append(ret, mf.ownMilestones[vid].WrappedOutput)

			//p.setTraceNAhead(1)
			//p.trace("DO insert milestone into future cone: %s", vid.IDShort())
		} else {
			//p.setTraceNAhead(1)
			//p.trace("DO NOT insert milestone into future cone: %s", vid.IDShort())
		}
	}
	return ret
}

// ownForksInAnotherSequencerPastCone sorted by coverage descending
func (mf *milestoneFactory) ownForksInAnotherSequencerPastCone(anotherSeqMs *utangle.WrappedTx, p proposerTask) []utangle.WrappedOutput {
	stateRdr := mf.tangle.MustGetBaselineState(anotherSeqMs)

	anotherSeqID := anotherSeqMs.MustSequencerID()
	rdr := multistate.MakeSugared(stateRdr)
	rootOutput, err := rdr.GetChainOutput(&mf.tipPool.chainID)
	if errors.Is(err, multistate.ErrNotFound) {
		// cannot find own seqID in the state of anotherSeqID. The tree is empty
		p.trace("cannot find own seqID %s in the state of another seq %s (%s). The tree is empty",
			mf.tipPool.chainID.VeryShort(), anotherSeqMs.IDShort(), anotherSeqID.VeryShort())
		return nil
	}
	util.AssertNoError(err)
	p.trace("found own seqID %s in the state of another seq %s (%s)",
		mf.tipPool.chainID.VeryShort(), anotherSeqMs.IDShort(), anotherSeqID.VeryShort())

	rootWrapped, ok, _ := mf.tangle.GetWrappedOutput(&rootOutput.ID, rdr)
	if !ok {
		p.trace("cannot fetch wrapped root output %s", rootOutput.IDShort())
		return nil
	}
	mf.addOwnMilestone(rootWrapped) // to ensure it is among own milestones
	return mf.futureConeMilestonesOrdered(rootWrapped.VID, p)
}

// makeAdditionalInputsOutputs makes additional outputs according to commands in imputs.
// Filters consumedInThePastPath so that transfer commands would not exceed maximumTotal
func (mf *milestoneFactory) makeAdditionalInputsOutputs(inputs []*core.OutputWithID, maximumTotal uint64) ([]*core.OutputWithID, []*core.Output) {
	retImp := make([]*core.OutputWithID, 0)
	retOut := make([]*core.Output, 0)

	myAddr := core.AddressED25519FromPrivateKey(mf.controllerKey)
	total := uint64(0)
	for _, inp := range inputs {
		if cmdData := parseSenderCommandDataRaw(myAddr, inp); len(cmdData) > 0 {
			o, err := makeOutputFromCommandData(cmdData)
			if err != nil {
				mf.log.Warnf("error while parsing sequencer command in input %s: %v", inp.IDShort(), err)
				continue
			}
			if o.Amount() <= maximumTotal-total {
				retImp = append(retImp, inp)
				retOut = append(retOut, o)
			}
		} else {
			retImp = append(retImp, inp)
		}
	}
	util.Assertf(len(retOut) <= maxAdditionalOutputs, "len(ret)<=maxAdditionalOutputs")
	return retImp, retOut
}

func (mf *milestoneFactory) getStatsAndReset() (ret factoryStats) {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	ret = factoryStats{
		numOwnMilestones:            len(mf.ownMilestones),
		ownMilestoneCount:           mf.ownMilestoneCount,
		removedMilestonesSinceReset: mf.removedMilestonesSinceReset,
		tipPoolStats:                mf.tipPool.getStatsAndReset(),
	}
	mf.removedMilestonesSinceReset = 0
	return
}
