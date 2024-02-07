package sequencer

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/factory"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/zap"
)

type (
	Sequencer struct {
		*workflow.Workflow
		ctx            context.Context
		stopFun        context.CancelFunc
		waitStop       sync.WaitGroup
		sequencerID    ledger.ChainID
		controllerKey  ed25519.PrivateKey
		config         *ConfigOptions
		log            *zap.SugaredLogger
		factory        *factory.MilestoneFactory
		milestoneCount int
		branchCount    int
		prevTimeTarget ledger.Time
		infoMutex      sync.RWMutex
		info           Info
		//
		onMilestoneSubmittedMutex sync.RWMutex
		onMilestoneSubmitted      func(seq *Sequencer, vid *vertex.WrappedTx)
	}

	Info struct {
		In                     int
		Out                    int
		InflationAmount        uint64
		NumConsumedFeeOutputs  int
		NumFeeOutputsInTippool int
		NumOtherMsInTippool    int
		LedgerCoverage         uint64
		PrevLedgerCoverage     uint64
	}
)

const TraceTag = "sequencer"

func New(glb *workflow.Workflow, seqID ledger.ChainID, controllerKey ed25519.PrivateKey, ctx context.Context, opts ...ConfigOption) (*Sequencer, error) {
	cfg := makeConfig(opts...)
	ret := &Sequencer{
		Workflow:      glb,
		sequencerID:   seqID,
		controllerKey: controllerKey,
		config:        cfg,
		log:           glb.Log().Named(fmt.Sprintf("%s-%s", cfg.SequencerName, seqID.StringVeryShort())),
	}
	ret.ctx, ret.stopFun = context.WithCancel(ctx)

	var err error
	if ret.factory, err = factory.New(ret, cfg.MaxFeeInputs); err != nil {
		return nil, err
	}
	ret.Log().Infof("sequencer created with controller %s", ledger.AddressED25519FromPrivateKey(controllerKey).String())
	return ret, nil
}

func (seq *Sequencer) Start() {
	seq.waitStop.Add(1)

	runFun := func() {
		defer seq.waitStop.Done()

		if !seq.ensureFirstMilestone() {
			seq.log.Warnf("can't start sequencer. EXIT..")
			return
		}
		seq.mainLoop()
	}

	const debuggerFriendly = true
	if debuggerFriendly {
		go runFun()
	} else {
		util.RunWrappedRoutine(seq.config.SequencerName+"[mainLoop]", runFun, func(err error) {
			seq.log.Fatal(err)
		}, common.ErrDBUnavailable)

	}
}

const ensureStartingMilestoneTimeout = time.Second

func (seq *Sequencer) ensureFirstMilestone() bool {
	ctx, cancel := context.WithTimeout(seq.ctx, ensureStartingMilestoneTimeout)
	var startingMilestoneOutput vertex.WrappedOutput

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
				startingMilestoneOutput = seq.factory.OwnLatestMilestoneOutput()
				if startingMilestoneOutput.VID != nil {
					cancel()
					return
				}
			}
		}
	}()
	<-ctx.Done()
	if startingMilestoneOutput.VID == nil {
		seq.log.Errorf("failed to find a milestone to start")
		return false
	}
	amount, lock, err := startingMilestoneOutput.AmountAndLock()
	if err != nil {
		seq.log.Errorf("sequencer start output %s is not available: %v", startingMilestoneOutput.IDShortString(), err)
		return false
	}
	if !lock.UnlockableWith(ledger.AddressED25519FromPrivateKey(seq.controllerKey).AccountID()) {
		seq.log.Errorf("provided private key does match sequencer lock %s", lock.String())
		return false
	}
	seq.log.Infof("sequencer will start with the milestone output %s and amount %s",
		startingMilestoneOutput.IDShortString(), util.GoTh(amount))

	sleepDuration := ledger.SleepDurationUntilFutureLedgerTime(startingMilestoneOutput.Timestamp())
	if sleepDuration > 0 {
		seq.log.Infof("will delay start for %v to sync starting milestone with the real clock", sleepDuration)
		time.Sleep(sleepDuration)
	}
	return true
}

func (seq *Sequencer) Stop() {
	seq.stopFun()
}

func (seq *Sequencer) StopAndWait() {
	seq.stopFun()
	seq.waitStop.Wait()
}

func (seq *Sequencer) WaitStop() {
	seq.waitStop.Wait()
}

func (seq *Sequencer) SequencerID() ledger.ChainID {
	return seq.sequencerID
}

func (seq *Sequencer) ControllerPrivateKey() ed25519.PrivateKey {
	return seq.controllerKey
}

func (seq *Sequencer) Context() context.Context {
	return seq.ctx
}

func (seq *Sequencer) SequencerName() string {
	return seq.config.SequencerName
}

func (seq *Sequencer) Log() *zap.SugaredLogger {
	return seq.log
}

func (seq *Sequencer) Tracef(tag string, format string, args ...any) {
	seq.Workflow.TraceLog(seq.log, tag, format, args...)
}

func (seq *Sequencer) mainLoop() {
	beginAt := seq.Workflow.SyncData().WhenStarted().Add(seq.config.DelayStart)
	if beginAt.After(time.Now()) {
		seq.log.Infof("wait for %v before starting the main loop", seq.config.DelayStart)
	}
	time.Sleep(time.Until(beginAt))

	seq.Log().Infof("STARTING sequencer")
	defer func() {
		seq.Log().Infof("sequencer STOPPING..")
		_ = seq.Log().Sync()
	}()

	for {
		select {
		case <-seq.ctx.Done():
			return
		default:
			if !seq.doSequencerStep() {
				return
			}
		}
	}
}

func (seq *Sequencer) doSequencerStep() bool {
	seq.Tracef(TraceTag, "doSequencerStep")
	if seq.config.MaxMilestones != 0 && seq.milestoneCount >= seq.config.MaxMilestones {
		seq.log.Infof("reached max limit of milestones %d -> stopping", seq.config.MaxMilestones)
		return false
	}
	if seq.config.MaxBranches != 0 && seq.branchCount >= seq.config.MaxBranches {
		seq.log.Infof("reached max limit of branch milestones %d -> stopping", seq.config.MaxBranches)
		return false
	}

	timerStart := time.Now()

	targetTs := seq.getNextTargetTime()
	util.Assertf(!targetTs.Before(seq.prevTimeTarget), "wrong target ts %s: must not be before previous target %s",
		targetTs.String(), seq.prevTimeTarget.String())
	seq.prevTimeTarget = targetTs

	if seq.config.MaxTargetTs != ledger.NilLedgerTime && targetTs.After(seq.config.MaxTargetTs) {
		seq.log.Infof("next target ts %s is after maximum ts %s -> stopping", targetTs, seq.config.MaxTargetTs)
		return false
	}

	seq.Tracef(TraceTag, "target ts: %s. Now is: %s", targetTs, ledger.TimeNow())

	msTx := seq.factory.StartProposingForTargetLogicalTime(targetTs)
	if msTx == nil {
		seq.Tracef(TraceTag, "failed to generate msTx for target %s. Now is %s", targetTs, ledger.TimeNow())
		time.Sleep(10 * time.Millisecond)
		return true
	}

	seq.Tracef(TraceTag, "produced milestone %s for the target logical time %s in %v",
		msTx.IDShortString(), targetTs, time.Since(timerStart))

	msVID := seq.submitMilestone(msTx)
	if msVID == nil {
		return true
	}

	seq.factory.AddOwnMilestone(msVID)
	seq.milestoneCount++
	if msVID.IsBranchTransaction() {
		seq.branchCount++
	}
	seq.updateInfo(msVID)
	seq.runOnMilestoneSubmitted(msVID)
	return true
}

const sleepWaitingCurrentMilestoneTime = 10 * time.Millisecond

func (seq *Sequencer) getNextTargetTime() ledger.Time {
	var prevMilestoneTs ledger.Time

	currentMsOutput := seq.factory.OwnLatestMilestoneOutput()
	util.Assertf(currentMsOutput.VID != nil, "currentMsOutput.VID != nil")
	prevMilestoneTs = currentMsOutput.Timestamp()

	// synchronize clock
	nowis := ledger.TimeNow()
	if nowis.Before(prevMilestoneTs) {
		waitDuration := time.Duration(ledger.DiffTicks(prevMilestoneTs, nowis)) * ledger.TickDuration()
		seq.log.Warnf("nowis (%s) is before last milestone ts (%s). Sleep %v",
			nowis.String(), prevMilestoneTs.String(), waitDuration)
		time.Sleep(waitDuration)
	}
	nowis = ledger.TimeNow()
	for ; nowis.Before(prevMilestoneTs); nowis = ledger.TimeNow() {
		seq.log.Warnf("nowis (%s) is before last milestone ts (%s). Sleep %v",
			nowis.String(), prevMilestoneTs.String(), sleepWaitingCurrentMilestoneTime)
		time.Sleep(sleepWaitingCurrentMilestoneTime)
	}
	// logical time now is approximately equal to the clock time
	nowis = ledger.TimeNow()
	util.Assertf(!nowis.Before(prevMilestoneTs), "!core.TimeNow().Before(prevMilestoneTs)")

	// TODO taking into account average speed of proposal generation

	targetAbsoluteMinimum := prevMilestoneTs.AddTicks(seq.config.Pace)
	nextSlotBoundary := nowis.NextTimeSlotBoundary()

	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum
	}
	// absolute minimum is before the next slot boundary
	// set absolute minimum starting from now
	minimumTicksAheadFromNow := (seq.config.Pace * 2) / 3 // seq.config.Pace
	targetAbsoluteMinimum = nowis.AddTicks(minimumTicksAheadFromNow)
	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum
	}

	if targetAbsoluteMinimum.TimesTicksToNextSlotBoundary() <= seq.config.Pace {
		return nextSlotBoundary
	}

	return targetAbsoluteMinimum
}

// Returns nil if fails to generate acceptable bestSoFarTx until the deadline
func (seq *Sequencer) generateNextMilestoneTxForTargetTime(targetTs ledger.Time) *transaction.Transaction {
	seq.Tracef(TraceTag, "generateNextMilestoneTxForTargetTime %s", targetTs)

	timeout := time.Duration(seq.config.Pace) * ledger.TickDuration()
	absoluteDeadline := targetTs.Time().Add(timeout)

	if absoluteDeadline.Before(time.Now()) {
		// too late, was too slow, failed to meet the target deadline
		seq.log.Warnf("didn't start proposers for target %s: nowis %v, too late for absolute deadline %v",
			targetTs.String(), time.Now(), absoluteDeadline)
		return nil
	}

	msTx := seq.factory.StartProposingForTargetLogicalTime(targetTs)
	if msTx != nil {
		util.Assertf(msTx.Timestamp() == targetTs, "msTx.Timestamp() (%v) == targetTs (%v)", msTx.Timestamp(), targetTs)
	}
	return msTx
}

const submitTimeout = 5 * time.Second

func (seq *Sequencer) submitMilestone(tx *transaction.Transaction) *vertex.WrappedTx {
	seq.Tracef(TraceTag, "submit new milestone %s", tx.IDShortString)
	deadline := time.Now().Add(submitTimeout)
	vid, err := seq.SequencerMilestoneAttachWait(tx.Bytes(), submitTimeout)
	if err != nil {
		seq.Log().Errorf("failed to submit new milestone %s: '%v'", tx.IDShortString(), err)
		return nil
	}

	seq.Tracef(TraceTag, "new milestone %s submitted successfully", tx.IDShortString)
	if !seq.waitMilestoneInTippool(vid, deadline) {
		seq.Log().Errorf("timed out while waiting %v for submitted milestone %s in the tippool", submitTimeout, vid.IDShortString())
		return nil
	}
	return vid
}

func (seq *Sequencer) waitMilestoneInTippool(vid *vertex.WrappedTx, deadline time.Time) bool {
	for {
		if time.Now().After(deadline) {
			return false
		}
		if seq.factory.OwnLatestMilestoneOutput().VID == vid {
			return true
		}
		time.Sleep(1 * time.Millisecond)
	}
}

func (seq *Sequencer) OnMilestoneSubmitted(fun func(seq *Sequencer, ms *vertex.WrappedTx)) {
	seq.onMilestoneSubmittedMutex.Lock()
	defer seq.onMilestoneSubmittedMutex.Unlock()

	if seq.onMilestoneSubmitted == nil {
		seq.onMilestoneSubmitted = fun
	} else {
		prevFun := seq.onMilestoneSubmitted
		seq.onMilestoneSubmitted = func(seq *Sequencer, ms *vertex.WrappedTx) {
			prevFun(seq, ms)
			fun(seq, ms)
		}
	}
}

func (seq *Sequencer) runOnMilestoneSubmitted(ms *vertex.WrappedTx) {
	seq.onMilestoneSubmittedMutex.RLock()
	defer seq.onMilestoneSubmittedMutex.RUnlock()

	if seq.onMilestoneSubmitted != nil {
		seq.onMilestoneSubmitted(seq, ms)
	}
}
