package sequencer

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/sequencer/task"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap"
)

type (
	Environment interface {
		global.NodeGlobal
		attacher.Environment
		IsSynced() bool
		TxBytesStore() global.TxBytesStore
		SequencerMilestoneAttachWait(txBytes []byte, meta *txmetadata.TransactionMetadata, timeout time.Duration) (*vertex.WrappedTx, error)
		GetLatestMilestone(seqID ledger.ChainID) *vertex.WrappedTx
		LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		NumSequencerTips() int
		ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput))
		MustEnsureBranch(txid ledger.TransactionID) *vertex.WrappedTx
	}

	Sequencer struct {
		Environment
		ctx                context.Context    // local context
		stopFun            context.CancelFunc // local stop function
		sequencerID        ledger.ChainID
		controllerKey      ed25519.PrivateKey
		backlog            *backlog.InputBacklog
		config             *ConfigOptions
		logName            string
		log                *zap.SugaredLogger
		ownMilestonesMutex sync.RWMutex
		ownMilestones      map[*vertex.WrappedTx]outputsWithTime // map ms -> consumed outputs in the past

		milestoneCount  int
		branchCount     int
		lastSubmittedTs ledger.Time
		infoMutex       sync.RWMutex
		info            Info
		//
		onCallbackMutex      sync.RWMutex
		onMilestoneSubmitted func(seq *Sequencer, vid *vertex.WrappedTx)
		onExit               func()

		*slotStats
	}

	outputsWithTime struct {
		consumed set.Set[vertex.WrappedOutput]
		since    time.Time
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

func New(env Environment, seqID ledger.ChainID, controllerKey ed25519.PrivateKey, opts ...ConfigOption) (*Sequencer, error) {
	cfg := configOptions(opts...)
	logName := fmt.Sprintf("[%s-%s]", cfg.SequencerName, seqID.StringVeryShort())
	ret := &Sequencer{
		Environment:   env,
		sequencerID:   seqID,
		controllerKey: controllerKey,
		ownMilestones: make(map[*vertex.WrappedTx]outputsWithTime),
		config:        cfg,
		logName:       logName,
		log:           env.Log().Named(logName),
		slotStats:     util.Ref(_newSlotStats(0)),
	}
	ret.ctx, ret.stopFun = context.WithCancel(env.Ctx())
	var err error

	if ret.backlog, err = backlog.New(ret); err != nil {
		return nil, err
	}
	if err = ret.backlog.LoadSequencerStartTips(seqID); err != nil {
		return nil, err
	}
	ret.Log().Infof("sequencer is starting with config:\n%s", cfg.lines(seqID, ledger.AddressED25519FromPrivateKey(controllerKey), "     ").String())
	return ret, nil
}

func NewFromConfig(glb *workflow.Workflow) (*Sequencer, error) {
	cfg, seqID, controllerKey, err := paramsFromConfig()
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

		if !seq.ensureFirstMilestone() {
			seq.log.Warnf("can't start sequencer. EXIT..")
			return
		}
		seq.log.Infof("waiting for %v (1 slot) before starting sequencer", ledger.L().ID.SlotDuration())
		time.Sleep(ledger.L().ID.SlotDuration())

		seq.log.Infof("sequencer has been STARTED %s", util.Ref(seq.SequencerID()).String())

		ttl := time.Duration(seq.config.MilestonesTTLSlots) * ledger.L().ID.SlotDuration()
		seq.RepeatInBackground(seq.SequencerName()+"_own_milestone_purge", ownMilestonePurgePeriod, func() bool {
			if n, remain := seq.purgeOwnMilestones(ttl); n > 0 {
				if seq.VerbosityLevel() > 0 {
					seq.Log().Infof("purged %d own milestones, %d remain", n, remain)
				}
			}
			return true
		}, true)

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

func (seq *Sequencer) Ctx() context.Context {
	return seq.ctx
}

func (seq *Sequencer) Stop() {
	seq.stopFun()
}

const ensureStartingMilestoneTimeout = 2 * time.Second

// ensureFirstMilestone waiting for the first sequencer milestone arrive
func (seq *Sequencer) ensureFirstMilestone() bool {
	ctx, cancel := context.WithTimeout(seq.Ctx(), ensureStartingMilestoneTimeout)
	var startOutput vertex.WrappedOutput

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
				startOutput = seq.OwnLatestMilestoneOutput()
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
	seq.AddOwnMilestone(startOutput.VID)

	if sleepDuration := time.Until(startOutput.Timestamp().Time()); sleepDuration > 0 {
		seq.log.Warnf("will delay start for %v to sync ledger time with the clock", sleepDuration)
		seq.ClockCatchUpWithLedgerTime(startOutput.Timestamp())
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
		seq.log.Errorf("checkSequencerStartOutput: failed to load start output %s: %s", wOut.IDShortString(), err)
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
			start := time.Now()
			if !seq.doSequencerStep() {
				return
			}
			duration := time.Since(start)
			if duration > 3*time.Second {
				seq.Log().Warnf(">>>>>>>>>>>>> sequencer step took %v", duration)
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
	seq.StatsNewTarget()

	seq.Assertf(ledger.ValidSequencerPace(seq.lastSubmittedTs, targetTs), "target is closer than allowed pace (%d): %s -> %s",
		ledger.TransactionPaceSequencer(), seq.lastSubmittedTs.String, targetTs.String)

	seq.Assertf(targetTs.After(seq.lastSubmittedTs), "wrong target ts %s: should be after previous submitted %s",
		targetTs.String, seq.lastSubmittedTs.String)

	if seq.config.MaxTargetTs != ledger.NilLedgerTime && targetTs.After(seq.config.MaxTargetTs) {
		seq.log.Infof("next target ts %s is after maximum ts %s -> stopping", targetTs, seq.config.MaxTargetTs)
		return false
	}

	seq.Tracef(TraceTag, "target ts: %s. Now is: %s", targetTs, ledger.TimeNow())

	msTx, meta, err := seq.generateMilestoneForTarget(targetTs)
	switch {
	case errors.Is(err, task.ErrNotGoodEnough):
		seq.StatsNotGoodEnough()
		return true
	case errors.Is(err, task.ErrNoProposals):
		seq.StatsNoProposals()
		return true
	case err != nil:
		seq.Log().Warnf("FAILED to generate transaction for target %s. Now is %s. Reason: %v",
			targetTs, ledger.TimeNow(), err)
		return true
	}
	util.Assertf(msTx != nil, "msTx != nil")

	seq.Tracef(TraceTag, "produced milestone %s for the target logical time %s in %v. Meta: %s",
		msTx.IDShortString, targetTs, time.Since(timerStart), meta.String)

	saveLastSubmittedTs := seq.lastSubmittedTs

	msVID := seq.submitMilestone(msTx, meta)
	if msVID != nil {
		if saveLastSubmittedTs.IsSlotBoundary() && msVID.ID.Timestamp().IsSlotBoundary() {
			seq.Log().Warnf("branch jumped over the slot: %s -> %s. Step started: %s, %d (%s), %v ago, nowis: %s",
				saveLastSubmittedTs.String(), targetTs.String(),
				timerStart.Format(time.StampNano), timerStart.UnixNano(), ledger.TimeFromClockTime(timerStart).String(), time.Since(timerStart),
				ledger.TimeNow().String())
		}

		seq.AddOwnMilestone(msVID)
		seq.milestoneCount++
		if msVID.IsBranchTransaction() {
			seq.branchCount++
			seq.StatsBranchTxSubmitted(&msVID.ID)
		} else {
			seq.StatsSequencerTxSubmitted(&msVID.ID)
		}
		seq.updateInfo(msVID)
		seq.runOnMilestoneSubmitted(msVID)
	}

	if targetTs.IsSlotBoundary() {
		seq.Log().Infof("SLOT STATS: %s", seq.slotStats.Lines().Join(", "))
		seq.StatsReset(targetTs.Slot())
	}
	return true
}

func (seq *Sequencer) getNextTargetTime() ledger.Time {
	// wait to catch up with ledger time
	seq.ClockCatchUpWithLedgerTime(seq.lastSubmittedTs)

	nowis := ledger.TimeNow()
	var targetAbsoluteMinimum ledger.Time

	if seq.lastSubmittedTs.IsSlotBoundary() {
		// TODO experimental. Top avoid closeness bias. Put constant into the ledger ID

		const afterBranchPaceMultiplier = 3
		targetAbsoluteMinimum = seq.lastSubmittedTs.AddTicks(afterBranchPaceMultiplier * seq.config.Pace)
	} else {
		targetAbsoluteMinimum = ledger.MaximumTime(
			seq.lastSubmittedTs.AddTicks(seq.config.Pace),
			nowis.AddTicks(1),
		)
	}
	nextSlotBoundary := nowis.NextSlotBoundary()

	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum
	}
	// absolute minimum is before the next slot boundary, take the time now as a baseline
	minimumTicksAheadFromNow := (seq.config.Pace * 2) / 3 // seq.config.Pace
	targetAbsoluteMinimum = ledger.MaximumTime(targetAbsoluteMinimum, nowis.AddTicks(minimumTicksAheadFromNow))
	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum
	}

	if targetAbsoluteMinimum.TicksToNextSlotBoundary() <= seq.config.Pace {
		return ledger.MaximumTime(nextSlotBoundary, targetAbsoluteMinimum)
	}

	return targetAbsoluteMinimum
}

// TODO optimize very verbose logging when not able to issue branch. decideSubmitMilestone and base proposer

// decideSubmitMilestone branch transactions are issued only if healthy, or bootstrap mode enabled
func (seq *Sequencer) decideSubmitMilestone(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata) bool {
	if tx.IsBranchTransaction() {
		healthy := global.IsHealthyCoverage(*meta.LedgerCoverage, *meta.Supply, global.FractionHealthyBranch)
		bootstrapMode := seq.IsBootstrapMode()
		if healthy || bootstrapMode {
			seq.Log().Infof("SUBMIT BRANCH %s. Ledger time now: %s, proposer: %s, healthy: %v, bootstrap mode: %v, coverage: %s",
				tx.IDShortString(), ledger.TimeNow().String(), tx.SequencerTransactionData().SequencerOutputData.MilestoneData.Name,
				healthy, bootstrapMode, util.Th(*meta.LedgerCoverage))
			return true
		}
		seq.Log().Infof("WON'T SUBMIT BRANCH %s. Ledger time now: %s, proposer: %s, healthy: %v, bootstrap mode: %v, coverage: %s",
			tx.IDShortString(), ledger.TimeNow().String(), tx.SequencerTransactionData().SequencerOutputData.MilestoneData.Name,
			healthy, bootstrapMode, util.Th(*meta.LedgerCoverage))
		return false
	}

	seq.Log().Infof("SUBMIT SEQ TX %s. Ledger time now: %s, proposer: %s, coverage: %s",
		tx.IDShortString(), ledger.TimeNow().String(), tx.SequencerTransactionData().SequencerOutputData.MilestoneData.Name,
		util.Th(*meta.LedgerCoverage))
	return true
}

func (seq *Sequencer) submitMilestone(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata) *vertex.WrappedTx {
	if !seq.decideSubmitMilestone(tx, meta) {
		return nil
	}

	const submitTimeout = 5 * time.Second

	deadline := time.Now().Add(submitTimeout)
	vid, err := seq.SequencerMilestoneAttachWait(tx.Bytes(), meta, submitTimeout)
	if err != nil {
		seq.Log().Errorf("failed to submit new milestone %s: '%v'", tx.IDShortString(), err)

		//seq.savePastConeOfFailedTx(tx, 3)
		return nil
	}
	util.Assertf(vid != nil, "submitMilestone: vid != nil")

	seq.Tracef(TraceTag, "new milestone %s submitted successfully", tx.IDShortString)

	if err = seq.waitMilestoneInTippool(vid, deadline); err != nil {
		seq.Log().Error(err)
		return nil
	}
	seq.lastSubmittedTs = vid.Timestamp()
	return vid
}

func (seq *Sequencer) waitMilestoneInTippool(vid *vertex.WrappedTx, deadline time.Time) error {
	for {
		select {
		case <-seq.Ctx().Done():
			return fmt.Errorf("waitMilestoneInTippool: %s has been cancelled", vid.IDShortString())
		case <-time.After(10 * time.Millisecond):
			if time.Now().After(deadline) {
				return fmt.Errorf("waitMilestoneInTippool: deadline has been missed while waiting for %s in the tippool", vid.IDShortString())
			}
		default:
			if seq.GetLatestMilestone(seq.sequencerID) == vid {
				return nil
			}
		}
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

func (seq *Sequencer) MaxTagAlongInputs() int {
	return seq.config.MaxTagAlongInputs
}

func (seq *Sequencer) BacklogTTLSlots() int {
	return seq.config.BacklogTTLSlots
}

func (seq *Sequencer) bootstrapOwnMilestoneOutput() vertex.WrappedOutput {
	milestones := seq.LatestMilestonesDescending()
	for _, ms := range milestones {
		baseline := ms.BaselineBranch()
		if baseline == nil {
			continue
		}
		rdr := multistate.MakeSugared(seq.GetStateReaderForTheBranch(&baseline.ID))
		chainOut, _, err := rdr.GetChainTips(&seq.sequencerID)
		if errors.Is(err, multistate.ErrNotFound) {
			continue
		}
		seq.AssertNoError(err)

		ret, err := attacher.AttachOutputWithID(chainOut, seq, attacher.WithInvokedBy("tippool"))
		seq.AssertNoError(err)
		return ret
	}
	return vertex.WrappedOutput{}
}

func (seq *Sequencer) generateMilestoneForTarget(targetTs ledger.Time) (*transaction.Transaction, *txmetadata.TransactionMetadata, error) {
	deadline := targetTs.Time()
	nowis := time.Now()
	seq.Tracef(TraceTag, "generateMilestoneForTarget: target: %s, deadline: %s, nowis: %s",
		targetTs.String, deadline.Format("15:04:05.999"), nowis.Format("15:04:05.999"))

	if deadline.Before(nowis) {
		return nil, nil, fmt.Errorf("target %s is in the past by %v: impossible to generate milestone",
			targetTs.String(), nowis.Sub(deadline))
	}
	return task.Run(seq, targetTs)
}

func (seq *Sequencer) NumOutputsInBuffer() int {
	return seq.Backlog().NumOutputsInBuffer()
}

func (seq *Sequencer) NumMilestones() int {
	return seq.NumSequencerTips()
}
