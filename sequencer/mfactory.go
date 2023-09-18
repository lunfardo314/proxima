package sequencer

import (
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	utangle "github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/testutil"
	"go.uber.org/zap"
)

type (
	milestoneFactory struct {
		mutex         sync.RWMutex
		log           *zap.SugaredLogger
		tangle        *utangle.UTXOTangle
		tipPool       *sequencerTipPool
		controllerKey ed25519.PrivateKey
		proposal      latestMilestoneProposal
		ownMilestones map[*utangle.WrappedTx]utangle.WrappedOutput
		lastMilestone utangle.WrappedOutput
		maxFeeInputs  int
		lastPruned    time.Time
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
	}
)

func createMilestoneFactory(par *configuration) (*milestoneFactory, error) {
	log := testutil.NewNamedLogger(fmt.Sprintf("[%sF-%s]", par.SequencerName, par.ChainID.VeryShort()), par.LogLevel)
	var chainOut, stemOut utangle.WrappedOutput
	var err error
	if par.ProvideStartOutputs != nil {
		chainOut, stemOut, err = par.ProvideStartOutputs()
	} else {
		chainOut, stemOut, err = par.Glb.UTXOTangle().LoadSequencerStartOutputsDefault(par.ChainID)
	}
	if err != nil {
		return nil, err
	}
	// creates sequencer output out of chain origin and tags along, if necessary
	chainOut, stemOut, created, err := ensureSequencerStartOutputs(chainOut, stemOut, par.Params)
	if err != nil {
		return nil, err
	}
	if created {
		log.Infof("created sequencer start output %s", chainOut.DecodeID().Short())
	}

	mempool, err := startMempool(par.SequencerName, par.Glb, par.ChainID, par.LogLevel)
	if err != nil {
		return nil, err
	}

	ret := &milestoneFactory{
		log:     log,
		tangle:  par.Glb.UTXOTangle(),
		tipPool: mempool,
		ownMilestones: map[*utangle.WrappedTx]utangle.WrappedOutput{
			chainOut.VID: chainOut,
		},
		lastMilestone: chainOut,
		controllerKey: par.ControllerKey,
		maxFeeInputs:  par.MaxFeeInputs,
	}
	if ret.maxFeeInputs == 0 {
		ret.maxFeeInputs = 254
	}
	ret.log.Debugf("milestone factory started")
	return ret, nil
}

func ensureSequencerStartOutputs(chainOut, stemOut utangle.WrappedOutput, par Params) (utangle.WrappedOutput, utangle.WrappedOutput, bool, error) {
	if chainOut.VID.IsSequencerMilestone() && par.ProvideTagAlongSequencers == nil {
		// chain has sequencer output is already at start
		return chainOut, stemOut, false, nil
	}

	// it is a plain chain output, without sequencer constraint. Need to create sequencer output
	// We take current branch transaction ID from stem
	// timestamp is
	// - current time if it fits the current slot
	// - last tick in the slot
	ts := core.MaxLogicalTime(core.LogicalTimeNow(), chainOut.Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks))
	if ts.TimeSlot() != stemOut.TimeSlot() {
		ts = core.MustNewLogicalTime(stemOut.TimeSlot(), core.TimeTicksPerSlot-1)
	}
	util.Assertf(core.ValidTimePace(chainOut.Timestamp(), ts), "core.ValidTimePace(chainOut.LogicalTime(), ts) %s, %s",
		chainOut.Timestamp(), ts)

	// to speed up finalization of the sequencer we optionally "bribe" some other sequencers by paying fees to them
	var feeOutputs []*core.Output
	if par.ProvideTagAlongSequencers != nil {
		bootstrapSequencerIDs, feeAmount := par.ProvideTagAlongSequencers()
		feeOutputs = make([]*core.Output, len(bootstrapSequencerIDs))
		for i := range feeOutputs {
			feeOutputs[i] = core.NewOutput(func(o *core.Output) {
				o.WithAmount(feeAmount).
					WithLock(core.ChainLockFromChainID(bootstrapSequencerIDs[i]))
			})
		}
	}
	chainOutWithID, err := chainOut.Unwrap()
	if err != nil || chainOutWithID == nil {
		return utangle.WrappedOutput{}, utangle.WrappedOutput{}, false, err
	}
	txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
		ChainInput: &core.OutputWithChainID{
			OutputWithID: *chainOutWithID,
			ChainID:      par.ChainID,
		},
		Timestamp:         ts,
		AdditionalOutputs: feeOutputs,
		Endorsements:      util.List(stemOut.VID.ID()),
		PrivateKey:        par.ControllerKey,
		TotalSupply:       0,
	})
	if err != nil {
		return utangle.WrappedOutput{}, utangle.WrappedOutput{}, false, err
	}

	vid, err := par.Glb.TransactionInWaitAppendSync(txBytes)
	if err != nil {
		return utangle.WrappedOutput{}, utangle.WrappedOutput{}, false, err
	}
	util.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")
	return *vid.SequencerOutput(), stemOut, true, nil
}

func (mf *milestoneFactory) trace(format string, args ...any) {
	if traceAll.Load() {
		mf.log.Infof("TRACE "+format, args...)
	}
}

func (mf *milestoneFactory) makeMilestone(chainIn, stemIn *utangle.WrappedOutput, feeInputs []utangle.WrappedOutput, endorse []*utangle.WrappedTx, targetTs core.LogicalTime) (*transaction.Transaction, error) {
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
	feeInputsReal := make([]*core.OutputWithID, len(feeInputs))
	for i, wOut := range feeInputs {
		feeInputsReal[i], err = wOut.Unwrap()
		if err != nil {
			return nil, err
		}
		if feeInputsReal[i] == nil {
			return nil, nil
		}
	}
	endorseReal := utangle.DecodeIDs(endorse...)

	if err != nil {
		return nil, err
	}
	txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
		ChainInput: &core.OutputWithChainID{
			OutputWithID: *chainInReal,
			ChainID:      mf.tipPool.ChainID(),
		},
		StemInput:        stemInReal,
		Timestamp:        targetTs,
		AdditionalInputs: feeInputsReal,
		Endorsements:     endorseReal,
		PrivateKey:       mf.controllerKey,
	})
	if err != nil {
		return nil, err
	}
	return transaction.FromBytesMainChecksWithOpt(txBytes)
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

func (mf *milestoneFactory) setLastMilestone(msOutput utangle.WrappedOutput) {
	mf.mutex.Lock()
	defer mf.mutex.Unlock()

	mf.lastMilestone = msOutput
	mf.ownMilestones[msOutput.VID] = msOutput
}

func (mf *milestoneFactory) getLastMilestone() utangle.WrappedOutput {
	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	return mf.ownMilestones[mf.lastMilestone.VID]
}

// setNewTarget signals proposer allMilestoneProposingStrategies about new timestamp,
// Returns last proposed proposal
func (mf *milestoneFactory) setNewTarget(ts core.LogicalTime) {
	mf.proposal.mutex.Lock()
	defer mf.proposal.mutex.Unlock()

	//if mf.proposal.targetTs.TimeTick() == 0 {
	//	mf.proposal.bestSoFar = nil // clearing baseline for comparison each slot
	//}
	mf.proposal.targetTs = ts
	mf.proposal.current = nil
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

func (mf *milestoneFactory) startProposingForTargetLogicalTime(targetTs core.LogicalTime) *utangle.WrappedOutput {
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

	ret := mf.proposal.getLatestProposal() // will return nil if wasn't able to generate transaction
	// set target time to nil -> signal workers to exit
	mf.setNewTarget(core.NilLogicalTime)
	return ret
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
		mf.log.Infof("removed orphaned milestone %s", vid.IDShort())
		delete(mf.ownMilestones, vid)
	}
}

func (mf *milestoneFactory) futureConeMilestonesOrdered(rootVID *utangle.WrappedTx) []utangle.WrappedOutput {
	mf.cleanOwnMilestonesIfNecessary()

	mf.mutex.RLock()
	defer mf.mutex.RUnlock()

	rootOut, ok := mf.ownMilestones[rootVID]
	if !ok {
		return nil
	}

	ordered := util.SortKeys(mf.ownMilestones, func(vid1, vid2 *utangle.WrappedTx) bool {
		// by timestamp -> equivalent to topological order, descending, i.e. older first
		return vid1.Timestamp().After(vid2.Timestamp())
	})

	visited := set.New[*utangle.WrappedTx](rootVID)
	ret := append(make([]utangle.WrappedOutput, 0, len(ordered)), rootOut)
	for _, vid := range ordered {
		if !vid.IsOrphaned() && visited.Contains(vid.SequencerPredecessor()) {
			visited.Insert(vid)
			ret = append(ret, mf.ownMilestones[vid])
		}
	}
	return ret
}

// ownForksInAnotherSequencerPastCone sorted by coverage descending
func (mf *milestoneFactory) ownForksInAnotherSequencerPastCone(anotherSeqVertex *utangle.WrappedTx) []utangle.WrappedOutput {
	stateRdr, available := mf.tangle.StateReaderOfSequencerMilestone(anotherSeqVertex)
	if !available {
		return nil
	}
	rootOutput, err := stateRdr.GetChainOutput(&mf.tipPool.chainID)
	if err != nil {
		// cannot find own seqID in the state of anotherSeqID. The tree is empty
		return nil
	}
	if !rootOutput.ID.IsSequencerTransaction() {
		return nil
	}
	txid := rootOutput.ID.TransactionID()
	rootVID, ok := mf.tangle.GetVertex(&txid)
	if !ok {
		return nil
	}
	return mf.futureConeMilestonesOrdered(rootVID)
}
