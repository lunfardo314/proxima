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
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	milestoneFactory struct {
		mutex                       sync.RWMutex
		log                         *zap.SugaredLogger
		tangle                      *utangle.UTXOTangle
		tipPool                     *sequencerTipPool
		controllerKey               ed25519.PrivateKey
		proposal                    latestMilestoneProposal
		ownMilestones               map[*utangle.WrappedTx]utangle.WrappedOutput
		maxFeeInputs                int
		lastPruned                  time.Time
		ownMilestoneCount           int
		removedMilestonesSinceReset int
	}

	milestoneWithData struct {
		utangle.WrappedOutput
		elapsed           time.Duration
		makeVertexElapsed time.Duration
		proposedBy        string
	}

	latestMilestoneProposal struct {
		mutex     sync.RWMutex
		targetTs  core.LogicalTime
		bestSoFar *utangle.WrappedOutput
		current   *utangle.WrappedOutput
		durations []time.Duration
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

	var chainOut utangle.WrappedOutput
	var err error
	if seq.config.StartupTxOptions != nil && seq.config.StartupTxOptions.ChainOutput != nil {
		var created bool
		// creates sequencer output out of chain origin and tags along, if necessary
		chainOut, created, err = seq.ensureSequencerStartOutput()
		if err != nil {
			return err
		}
		if created {
			log.Infof("created sequencer start output %s", chainOut.DecodeID().Short())
		}
	}

	ownMilestones := make(map[*utangle.WrappedTx]utangle.WrappedOutput)
	if chainOut.VID != nil {
		ownMilestones[chainOut.VID] = chainOut
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

func (seq *Sequencer) ensureSequencerStartOutput() (utangle.WrappedOutput, bool, error) {
	util.Assertf(seq.config.StartupTxOptions != nil && seq.config.StartupTxOptions.ChainOutput != nil, "ensureSequencerStartOutput: chain output not specified")

	if seq.config.StartupTxOptions.EndorseBranch == nil || !seq.config.StartupTxOptions.EndorseBranch.BranchFlagON() {
		return utangle.WrappedOutput{}, false, fmt.Errorf("ensureSequencerStartOutput: must endorse branch tx")
	}

	chainOut := seq.config.StartupTxOptions.ChainOutput
	// We take current branch transaction ID from stem
	// timestamp is
	// - current time if it fits the current slot
	// - last tick in the slot
	ts := core.MaxLogicalTime(core.LogicalTimeNow(), chainOut.Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks))
	endorse := seq.config.StartupTxOptions.EndorseBranch
	if endorse != nil && ts.TimeSlot() != endorse.TimeSlot() {
		ts = core.MustNewLogicalTime(endorse.TimeSlot(), core.TimeTicksPerSlot-1)
	}
	util.Assertf(core.ValidTimePace(chainOut.Timestamp(), ts), "core.ValidTimePace(chainOut.LogicalTime(), ts) %s, %s",
		chainOut.Timestamp(), ts)

	tagAlongSequencers := seq.config.StartupTxOptions.TagAlongSequencers
	tagAlongFeeOutputs := make([]*core.Output, len(tagAlongSequencers))
	for i := range tagAlongFeeOutputs {
		tagAlongFeeOutputs[i] = core.NewOutput(func(o *core.Output) {
			o.WithAmount(seq.config.StartupTxOptions.TagAlongFee).
				WithLock(core.ChainLockFromChainID(tagAlongSequencers[i]))
		})
	}
	chainOutWithID, err := chainOut.Unwrap()

	if chainOutWithID.Output.Amount() < core.MinimumAmountOnSequencer {
		return utangle.WrappedOutput{}, false, fmt.Errorf("ensureSequencerStartOutput: cannot start sequncer: not enough balance on chain output, must be at least %s",
			util.GoThousands(core.MinimumAmountOnSequencer))
	}

	if err != nil || chainOutWithID == nil {
		return utangle.WrappedOutput{}, false, err
	}
	txBytes, err := MakeSequencerTransaction(MakeSequencerTransactionParams{
		ChainInput: &core.OutputWithChainID{
			OutputWithID: *chainOutWithID,
			ChainID:      seq.chainID,
		},
		Timestamp:         ts,
		Endorsements:      []*core.TransactionID{endorse},
		AdditionalOutputs: tagAlongFeeOutputs,
		PrivateKey:        seq.controllerKey,
		TotalSupply:       0,
	})
	if err != nil {
		return utangle.WrappedOutput{}, false, err
	}

	vid, err := seq.glb.TransactionInWaitAppendSync(txBytes)
	if err != nil {
		return utangle.WrappedOutput{}, false, err
	}
	util.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")
	return *vid.SequencerOutput(), true, nil
}

func (mf *milestoneFactory) trace(format string, args ...any) {
	if traceAll.Load() {
		mf.log.Infof("TRACE "+format, args...)
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
	// interpret sequencer commands contained in fee inputs. This also possibly adjusts inputs
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
	txBytes, err := MakeSequencerTransaction(MakeSequencerTransactionParams{
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
	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	mf.ownMilestones[wOut.VID] = wOut
	mf.ownMilestoneCount++
}

// selectFeeInputs chooses unspent fee outputs which can be combined with seqMutations in one vid
// Quite expensive
func (mf *milestoneFactory) selectFeeInputs(seqDelta *utangle.UTXOStateDelta, targetTs core.LogicalTime) []utangle.WrappedOutput {
	util.Assertf(seqDelta != nil, "seqDelta != nil")

	selected := mf.tipPool.filterAndSortOutputs(func(o utangle.WrappedOutput) bool {
		if !core.ValidTimePace(o.Timestamp(), targetTs) {
			return false
		}
		if !seqDelta.CanBeConsumedBySequencer(o, mf.tangle) {
			return false
		}

		//fmt.Printf("******** suspicious false positive: %s\n%s\n***************\n", o.IDShort(), seqDelta.LinesRecursive().String())
		//seqDelta.CanBeConsumedBySequencer(o, mf.tangle)
		return true
	})
	ret := make([]utangle.WrappedOutput, 0, mf.maxFeeInputs)

	targetDelta := seqDelta.Clone()

	for _, o := range selected {
		o.VID.Unwrap(utangle.UnwrapOptions{
			Vertex: func(v *utangle.Vertex) {
				// cloning each time because MergeInto always mutates the target
				tmpTarget := targetDelta.Clone()
				if conflict, _ := v.StateDelta.MergeInto(tmpTarget); conflict == nil {
					ret = append(ret, o)
					targetDelta = tmpTarget
				}
			},
			VirtualTx: func(v *utangle.VirtualTransaction) {
				// do not need to clone because MustConsume does not mutate target in case of failure
				if success, _ := targetDelta.MustConsume(o); success {
					ret = append(ret, o)
				}
			},
		})
		if len(ret) >= mf.maxFeeInputs {
			break
		}
	}
	return ret
}

func (mf *milestoneFactory) getLatestMilestone() (ret utangle.WrappedOutput) {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	for _, ms := range mf.ownMilestones {
		if ret.VID == nil || ms.Timestamp().After(ret.Timestamp()) {
			ret = ms
		}
	}
	return ret
}

// setNewTarget signals proposer allMilestoneProposingStrategies about new timestamp,
// Returns last proposed proposal
func (mf *milestoneFactory) setNewTarget(ts core.LogicalTime) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	mf.proposal.targetTs = ts
	mf.proposal.current = nil
	mf.proposal.durations = make([]time.Duration, 0)
}

func (mf *milestoneFactory) storeProposalDuration(d time.Duration) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	mf.proposal.durations = append(mf.proposal.durations, d)
}

func (mf *milestoneFactory) averageProposalDuration() (time.Duration, int) {
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

func (mc *latestMilestoneProposal) getLatestProposal() *utangle.WrappedOutput {
	mc.mutex.RLock()
	defer mc.mutex.RUnlock()

	return mc.current
}

func (mf *milestoneFactory) startProposingForTargetLogicalTime(targetTs core.LogicalTime) (*utangle.WrappedOutput, time.Duration, int) {
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

	p.trace("futureConeMilestonesOrdered for root %s. Total %d own milestones",
		func() any { return rootVID.IDShort() }, len(mf.ownMilestones))

	rootOut, ok := mf.ownMilestones[rootVID]
	util.Assertf(ok, "futureConeMilestonesOrdered: milestone %s of chain %s is expected to be among set of own milestones (%d)",
		func() any { return rootVID.IDShort() },
		func() any { return mf.tipPool.chainID.Short() },
		len(mf.ownMilestones))

	ordered := util.SortKeys(mf.ownMilestones, func(vid1, vid2 *utangle.WrappedTx) bool {
		// by timestamp -> equivalent to topological order, descending, i.e. older first
		return vid1.Timestamp().After(vid2.Timestamp())
	})

	visited := set.New[*utangle.WrappedTx](rootVID)
	ret := append(make([]utangle.WrappedOutput, 0, len(ordered)), rootOut)
	for _, vid := range ordered {
		if !vid.IsOrphaned() && vid.IsSequencerMilestone() && visited.Contains(vid.SequencerPredecessor()) {
			visited.Insert(vid)
			ret = append(ret, mf.ownMilestones[vid])
		}
	}
	return ret
}

// ownForksInAnotherSequencerPastCone sorted by coverage descending
func (mf *milestoneFactory) ownForksInAnotherSequencerPastCone(anotherSeqVertex *utangle.WrappedTx, p proposerTask) []utangle.WrappedOutput {
	stateRdr, available := mf.tangle.StateReaderOfSequencerMilestone(anotherSeqVertex)
	if !available {
		p.trace("state reader not available for vertex %s", anotherSeqVertex.IDShort())
		return nil
	}
	rootOutput, err := stateRdr.GetChainOutput(&mf.tipPool.chainID)
	if errors.Is(err, multistate.ErrNotFound) {
		// cannot find own seqID in the state of anotherSeqID. The tree is empty
		p.trace("cannot find own seqID %s in the state of another seq %s. The tree is empty", mf.tipPool.chainID.Short(), anotherSeqVertex.IDShort())
		return nil
	}
	util.AssertNoError(err)
	rootWrapped, ok, _ := mf.tangle.GetWrappedOutput(&rootOutput.ID, func() multistate.SugaredStateReader {
		return stateRdr
	})
	if !ok {
		p.trace("cannot fetch wrapped root output %s", rootOutput.IDShort())
		return nil
	}
	mf.addOwnMilestone(rootWrapped) // to ensure it is among own milestones
	return mf.futureConeMilestonesOrdered(rootWrapped.VID, p)
}

// makeAdditionalInputsOutputs makes additional outputs according to commands in imputs.
// Filters inputs so that transfer commands would not exceed maximumTotal
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
