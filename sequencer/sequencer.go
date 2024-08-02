package sequencer

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/sequencer/factory"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"go.uber.org/zap"
)

type (
	Sequencer struct {
		*workflow.Workflow
		ctx             context.Context    // local context
		stopFun         context.CancelFunc // local stop function
		sequencerID     ledger.ChainID
		controllerKey   ed25519.PrivateKey
		config          *ConfigOptions
		logName         string
		log             *zap.SugaredLogger
		backlog         *backlog.InputBacklog
		factory         *factory.MilestoneFactory
		milestoneCount  int
		branchCount     int
		lastSubmittedTs ledger.Time
		infoMutex       sync.RWMutex
		info            Info
		//
		onCallbackMutex      sync.RWMutex
		onMilestoneSubmitted func(seq *Sequencer, vid *vertex.WrappedTx)
		onExit               func()
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

func New(glb *workflow.Workflow, seqID ledger.ChainID, controllerKey ed25519.PrivateKey, opts ...ConfigOption) (*Sequencer, error) {
	cfg := configOptions(opts...)
	logName := fmt.Sprintf("[%s-%s]", cfg.SequencerName, seqID.StringVeryShort())
	ret := &Sequencer{
		Workflow:      glb,
		sequencerID:   seqID,
		controllerKey: controllerKey,
		config:        cfg,
		logName:       logName,
		log:           glb.Log().Named(logName),
	}
	ret.ctx, ret.stopFun = context.WithCancel(glb.Ctx())
	var err error

	if ret.backlog, err = backlog.New(ret); err != nil {
		return nil, err
	}
	if ret.factory, err = factory.New(ret); err != nil {
		return nil, err
	}
	if err = ret.LoadSequencerTips(seqID); err != nil {
		return nil, err
	}
	ret.Log().Infof("sequencer is starting with config:\n%s", cfg.lines(seqID, ledger.AddressED25519FromPrivateKey(controllerKey), "     ").String())
	return ret, nil
}

func NewFromConfig(name string, glb *workflow.Workflow) (*Sequencer, error) {
	cfg, seqID, controllerKey, err := paramsFromConfig(name)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	return New(glb, seqID, controllerKey, cfg...)
}

func (seq *Sequencer) Start() {
	runFun := func() {
		seq.MarkWorkProcessStarted(seq.config.SequencerName)
		defer seq.MarkWorkProcessStopped(seq.config.SequencerName)

		if !seq.waitForSyncIfNecessary() {
			seq.log.Warnf("sequencer wasn't started. EXIT..")
			return
		}

		if !seq.ensureFirstMilestone() {
			seq.log.Warnf("can't start sequencer. EXIT..")
			return
		}
		seq.log.Infof("started sequencer '%s', seqID: %s", seq.SequencerName(), util.Ref(seq.SequencerID()).String())
		seq.sequencerLoop()

		seq.onCallbackMutex.RLock()
		defer seq.onCallbackMutex.RUnlock()

		if seq.onExit != nil {
			seq.onExit()
		}
	}

	const debuggerFriendly = false

	if debuggerFriendly {
		go runFun()
	} else {
		util.RunWrappedRoutine(seq.config.SequencerName+"[sequencerLoop]", runFun, func(err error) bool {
			seq.log.Fatal(err)
			return false
		})
	}
}

func (seq *Sequencer) waitForSyncIfNecessary() bool {
	if seq.IsBootstrapNode() {
		// bootstrap node does not wait for synced status
		return true
	}

	const checkSyncEvery = 3 * time.Second

	for {
		if synced, _ := seq.SyncStatus(); synced {
			break
		}
		select {
		case <-seq.Ctx().Done():
			return false
		case <-time.After(checkSyncEvery):
			seq.log.Warnf("waiting for sync with the network")
		}
	}

	return true
}

func (cfg *ConfigOptions) lines(seqID ledger.ChainID, controller ledger.AddressED25519, prefix ...string) *lines.Lines {
	return lines.New(prefix...).
		Add("ID: %s", seqID.String()).
		Add("Controller: %s", controller.String()).
		Add("Name: %s", cfg.SequencerName).
		Add("Pace: %d ticks", cfg.Pace).
		Add("MaxTagAlongInputs: %d", cfg.MaxTagAlongInputs).
		Add("MaxTargetTs: %s", cfg.MaxTargetTs.String()).
		Add("MaxSlots: %d", cfg.MaxBranches).
		Add("DelayStart: %v", cfg.DelayStart).
		Add("BacklogTTLSlots: %d", cfg.BacklogTTLSlots).
		Add("MilestoneTTLSlots: %d", cfg.MilestonesTTLSlots).
		Add("LogAttacherStats: %v", cfg.LogAttacherStats)
}

func (seq *Sequencer) Ctx() context.Context {
	return seq.ctx
}

func (seq *Sequencer) Stop() {
	seq.stopFun()
}

const ensureStartingMilestoneTimeout = time.Second

func (seq *Sequencer) ensureFirstMilestone() bool {
	ctx, cancel := context.WithTimeout(seq.Ctx(), ensureStartingMilestoneTimeout)
	var startOutput vertex.WrappedOutput

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
				startOutput = seq.factory.OwnLatestMilestoneOutput()
				if startOutput.VID != nil && startOutput.IsAvailable() {
					cancel()
					return
				}
			}
		}
	}()
	<-ctx.Done()

	if startOutput.VID == nil || !startOutput.IsAvailable() {
		seq.log.Errorf("failed to find a chain output to start")
		return false
	}
	if !seq.checkSequencerStartOutput(startOutput) {
		return false
	}
	seq.factory.AddOwnMilestone(startOutput.VID)

	sleepDuration := ledger.SleepDurationUntilFutureLedgerTime(startOutput.Timestamp())
	if sleepDuration > 0 {
		seq.log.Infof("will delay start for %v to sync starting milestone with the real clock", sleepDuration)
		time.Sleep(sleepDuration)
	}
	return true
}

func (seq *Sequencer) checkSequencerStartOutput(wOut vertex.WrappedOutput) bool {
	util.Assertf(wOut.VID != nil, "wOut.VID != nil")
	if !wOut.VID.ID.IsSequencerMilestone() {
		seq.log.Warnf("checkSequencerStartOutput: start output %s is not a sequencer output", wOut.IDShortString())
	}
	oReal, err := wOut.VID.OutputAt(wOut.Index)
	if oReal == nil || err != nil {
		seq.log.Errorf("checkSequencerStartOutput: failed to fetch start output %s: %s", wOut.IDShortString(), err)
		return false
	}
	lock := oReal.Lock()
	if !ledger.BelongsToAccount(lock, ledger.AddressED25519FromPrivateKey(seq.controllerKey)) {
		seq.log.Errorf("checkSequencerStartOutput: provided private key does match sequencer lock %s", lock.String())
		return false
	}
	seq.log.Infof("checkSequencerStartOutput: sequencer controller is %s", lock.String())

	amount := oReal.Amount()
	if amount < ledger.L().ID.MinimumAmountOnSequencer {
		seq.log.Errorf("checkSequencerStartOutput: amount %s on output is less than minimum %s required on sequencer",
			util.Th(amount), util.Th(ledger.L().ID.MinimumAmountOnSequencer))
		return false
	}
	seq.log.Infof("sequencer start output %s has amount %s (%s%% of the initial supply)",
		wOut.IDShortString(), util.Th(amount), util.PercentString(int(amount), int(ledger.L().ID.InitialSupply)))
	return true
}

func (seq *Sequencer) Backlog() *backlog.InputBacklog {
	return seq.backlog
}

func (seq *Sequencer) SequencerID() ledger.ChainID {
	return seq.sequencerID
}

func (seq *Sequencer) ControllerPrivateKey() ed25519.PrivateKey {
	return seq.controllerKey
}

func (seq *Sequencer) SequencerName() string {
	return seq.config.SequencerName
}

func (seq *Sequencer) Log() *zap.SugaredLogger {
	return seq.log
}

func (seq *Sequencer) sequencerLoop() {
	beginAt := time.Now().Add(seq.config.DelayStart)
	if seq.config.DelayStart > 0 {
		seq.log.Infof("wait for %v before starting the main loop", seq.config.DelayStart)
	}
	time.Sleep(time.Until(beginAt))

	seq.Log().Infof("STARTING sequencer loop")
	defer func() {
		seq.Log().Infof("sequencer loop STOPPING..")
		_ = seq.Log().Sync()
	}()

	for {
		select {
		case <-seq.Ctx().Done():
			return
		default:
			// checking condition if even makes sense to do a sequencer step. For bootstrap node is always makes sense
			// For non-bootstrap node it only makes sense if tippool is up-to-date
			if !seq.IsBootstrapNode() {
				if synced, behind := seq.SyncStatus(); !synced {
					if behind > 0 {
						seq.Log().Warnf("will not issue milestone: tippool is behind now by %d slots and it is not a bootstrap node", behind)
					} else {
						seq.Log().Warnf("will not issue milestone: tippool is empty and it is not a bootstrap node")
					}
					time.Sleep(ledger.L().ID.SlotDuration() / 2)
					continue
				}
			}

			if !seq.doSequencerStep() {
				return
			}
		}
	}
}

func (seq *Sequencer) doSequencerStep() bool {
	seq.Tracef(TraceTag, "doSequencerStep")
	if seq.config.MaxBranches != 0 && seq.branchCount >= seq.config.MaxBranches {
		seq.log.Infof("reached max limit of branch milestones %d -> stopping", seq.config.MaxBranches)
		return false
	}

	timerStart := time.Now()

	targetTs := seq.getNextTargetTime()

	seq.Assertf(ledger.ValidSequencerPace(seq.lastSubmittedTs, targetTs), "target is closer than allowed pace (%d): %s -> %s",
		ledger.TransactionPaceSequencer(), seq.lastSubmittedTs.String, targetTs.String)

	seq.Assertf(targetTs.After(seq.lastSubmittedTs), "wrong target ts %s: should be after previous submitted %s",
		targetTs.String(), seq.lastSubmittedTs.String)

	if seq.config.MaxTargetTs != ledger.NilLedgerTime && targetTs.After(seq.config.MaxTargetTs) {
		seq.log.Infof("next target ts %s is after maximum ts %s -> stopping", targetTs, seq.config.MaxTargetTs)
		return false
	}

	seq.Tracef(TraceTag, "target ts: %s. Now is: %s", targetTs, ledger.TimeNow())

	msTx, meta := seq.factory.StartProposingForTargetLogicalTime(targetTs)
	if msTx == nil {
		if targetTs.IsSlotBoundary() {
			seq.log.Infof("SKIPPED BRANCH on slot %d", targetTs.Slot())
		}
		seq.Tracef(TraceTag, "failed to generate msTx for target %s. Now is %s", targetTs, ledger.TimeNow())
		return true
	}

	seq.Tracef(TraceTag, "produced milestone %s for the target logical time %s in %v. Meta: %s",
		msTx.IDShortString, targetTs, time.Since(timerStart), meta.String)

	msVID := seq.submitMilestone(msTx, meta)
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
	// synchronize clock
	nowis := ledger.TimeNow()
	if nowis.Before(seq.lastSubmittedTs) {
		waitDuration := time.Duration(ledger.DiffTicks(seq.lastSubmittedTs, nowis)) * ledger.TickDuration()
		seq.log.Warnf("nowis (%s) is before last submitted ts (%s). Sleep %v",
			nowis.String(), seq.lastSubmittedTs.String(), waitDuration)
		time.Sleep(waitDuration)
	}
	nowis = ledger.TimeNow()
	for ; nowis.Before(seq.lastSubmittedTs); nowis = ledger.TimeNow() {
		seq.log.Warnf("nowis (%s) is before last milestone ts (%s). Sleep %v",
			nowis.String(), seq.lastSubmittedTs.String(), sleepWaitingCurrentMilestoneTime)
		time.Sleep(sleepWaitingCurrentMilestoneTime)
	}
	// ledger time now is approximately equal to the clock time
	nowis = ledger.TimeNow()
	seq.Assertf(!nowis.Before(seq.lastSubmittedTs), "!core.TimeNow().Before(prevMilestoneTs)")

	targetAbsoluteMinimum := ledger.MaxTime(
		seq.lastSubmittedTs.AddTicks(seq.config.Pace),
		nowis.AddTicks(1),
	)
	nextSlotBoundary := nowis.NextSlotBoundary()

	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum
	}
	// absolute minimum is before the next slot boundary, take the time now as a baseline
	minimumTicksAheadFromNow := (seq.config.Pace * 2) / 3 // seq.config.Pace
	targetAbsoluteMinimum = ledger.MaxTime(targetAbsoluteMinimum, nowis.AddTicks(minimumTicksAheadFromNow))
	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum
	}

	if targetAbsoluteMinimum.TicksToNextSlotBoundary() <= seq.config.Pace {
		return ledger.MaxTime(nextSlotBoundary, targetAbsoluteMinimum)
	}

	return targetAbsoluteMinimum
}

const submitTimeout = 10 * time.Second

func (seq *Sequencer) submitMilestone(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata) *vertex.WrappedTx {
	logMsg := fmt.Sprintf("%s SUBMIT milestone %s, proposer: %s",
		seq.logName, tx.IDShortString(), tx.SequencerTransactionData().SequencerOutputData.MilestoneData.Name)
	if seq.VerbosityLevel() > 0 {
		logMsg += ", " + meta.String()
	}
	seq.Log().Info(logMsg)

	deadline := time.Now().Add(submitTimeout)
	vid, err := seq.SequencerMilestoneAttachWait(tx.Bytes(), meta, submitTimeout)
	if err != nil {
		seq.Log().Errorf("failed to submit new milestone %s: '%v'", tx.IDShortString(), err)

		//seq.savePastConeOfFailedTx(tx, 3)

		return nil
	}

	seq.Tracef(TraceTag, "new milestone %s submitted successfully", tx.IDShortString)
	if !seq.waitMilestoneInTippool(vid, deadline) {
		seq.Log().Errorf("timed out while waiting %v for submitted milestone %s in the tippool", submitTimeout, vid.IDShortString())
		return nil
	}
	seq.lastSubmittedTs = vid.Timestamp()
	return vid
}

func (seq *Sequencer) savePastConeOfFailedTx(tx *transaction.Transaction, slotsBack int) {
	_, err := seq.TxBytesStore().PersistTxBytesWithMetadata(tx.Bytes(), nil)
	util.AssertNoError(err)
	memdag.SavePastConeFromTxStore(*tx.ID(), seq.TxBytesStore(), tx.Slot()-ledger.Slot(slotsBack), "cone-"+tx.ID().AsFileName())
}

func (seq *Sequencer) waitMilestoneInTippool(vid *vertex.WrappedTx, deadline time.Time) bool {
	for {
		if time.Now().After(deadline) {
			return false
		}
		if seq.GetLatestMilestone(seq.sequencerID) == vid {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (seq *Sequencer) OnMilestoneSubmitted(fun func(seq *Sequencer, ms *vertex.WrappedTx)) {
	seq.onCallbackMutex.Lock()
	defer seq.onCallbackMutex.Unlock()

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

func (seq *Sequencer) OnExit(fun func()) {
	seq.onCallbackMutex.Lock()
	defer seq.onCallbackMutex.Unlock()

	if seq.onExit == nil {
		seq.onExit = fun
	} else {
		prevFun := seq.onExit
		seq.onExit = func() {
			prevFun()
			fun()
		}
	}
}

func (seq *Sequencer) runOnMilestoneSubmitted(ms *vertex.WrappedTx) {
	seq.onCallbackMutex.RLock()
	defer seq.onCallbackMutex.RUnlock()

	if seq.onMilestoneSubmitted != nil {
		seq.onMilestoneSubmitted(seq, ms)
	}
}

func (seq *Sequencer) MaxTagAlongOutputs() int {
	return seq.config.MaxTagAlongInputs
}

func (seq *Sequencer) BacklogTTLSlots() int {
	return seq.config.BacklogTTLSlots
}

func (seq *Sequencer) MilestonesTTLSlots() int {
	return seq.config.MilestonesTTLSlots
}
