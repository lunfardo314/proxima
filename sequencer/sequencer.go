package sequencer

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
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
		sequencerID    ledger.ChainID
		controllerKey  ed25519.PrivateKey
		config         *ConfigOptions
		log            *zap.SugaredLogger
		factory        *factory.MilestoneFactory
		waitStop       sync.WaitGroup
		milestoneCount int
		branchCount    int
		prevTimeTarget ledger.LogicalTime
		infoMutex      sync.RWMutex
		info           Info
		//
		onMilestoneSubmittedMutex sync.RWMutex
		onMilestoneSubmitted      func(seq *Sequencer, vid *vertex.WrappedTx)

		//exit     atomic.Bool
		//stopWG   sync.WaitGroup
		//stopOnce sync.Once
		//
		//
		//traceNAhead    atomic.Int64
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

func New(glb *workflow.Workflow, seqID ledger.ChainID, controllerKey ed25519.PrivateKey, ctx context.Context, opts ...ConfigOption) (*Sequencer, error) {
	cfg := makeConfig(opts...)
	ret := &Sequencer{
		Workflow:      glb,
		ctx:           ctx,
		sequencerID:   seqID,
		controllerKey: controllerKey,
		config:        cfg,
		log:           glb.Log().Named(fmt.Sprintf("%s-%s", cfg.SequencerName, seqID.StringVeryShort())),
	}
	var err error
	if ret.factory, err = factory.New(ret, cfg.MaxFeeInputs); err != nil {
		return nil, err
	}
	return ret, nil
}

func (seq *Sequencer) Start() {
	if !seq.ensureFirstMilestone() {
		seq.log.Warnf("can't start sequencer")
		return
	}
	seq.waitStop.Add(1)
	util.RunWrappedRoutine(seq.config.SequencerName+"[mainLoop]", func() {
		seq.mainLoop()
		seq.waitStop.Done()
	}, func(err error) {
		seq.log.Fatal(err)
	}, common.ErrDBUnavailable)
}

const ensureStartingMilestoneTimeout = time.Second

func (seq *Sequencer) ensureFirstMilestone() bool {
	ctx, cancel := context.WithTimeout(seq.ctx, ensureStartingMilestoneTimeout)
	var startingMilestone *vertex.WrappedTx

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
				startingMilestone = seq.factory.OwnLatestMilestone()
				if startingMilestone != nil {
					cancel()
					return
				}
			}
		}
	}()
	<-ctx.Done()
	if startingMilestone == nil {
		seq.log.Errorf("failed to find a milestone to start")
		return false
	}
	seq.log.Infof("sequencer will start with the milestone %s", startingMilestone.IDShortString())
	return true
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
		seq.log.Infof("wait for one slot (%v) before starting the main loop", ledger.SlotDuration())
	}
	time.Sleep(time.Until(beginAt))
	seq.log.Infof("starting main loop")
	defer seq.log.Info("sequencer STOPPING..")

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
	seq.log.Info("doSequencerStep")
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

	if seq.config.MaxTargetTs != ledger.NilLogicalTime && targetTs.After(seq.config.MaxTargetTs) {
		seq.log.Infof("next target ts %s is after maximum ts %s -> stopping", targetTs, seq.config.MaxTargetTs)
		return false
	}

	seq.Tracef("seq", "target ts: %s. Now is: %s", targetTs, ledger.LogicalTimeNow())

	msTx := seq.factory.StartProposingForTargetLogicalTime(targetTs)
	if msTx == nil {
		seq.Tracef("seq", "failed to generate msTx for target: %s. Now is: %s", targetTs, ledger.LogicalTimeNow())
		time.Sleep(10 * time.Millisecond)
		return true
	}

	//seq.setTraceAhead(1)
	seq.Tracef("seq", "produced milestone %s for the target logical time %s in %v",
		msTx.IDShortString(), targetTs, time.Since(timerStart))

	msVID := seq.submitMilestone(msTx)

	if global.IsShuttingDown() {
		return false
	}
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

func (seq *Sequencer) getNextTargetTime() ledger.LogicalTime {
	var prevMilestoneTs ledger.LogicalTime

	currentMs := seq.factory.OwnLatestMilestone()
	util.Assertf(currentMs != nil, "currentMs != nil")
	prevMilestoneTs = currentMs.Timestamp()

	// synchronize clock
	nowis := ledger.LogicalTimeNow()
	if nowis.Before(prevMilestoneTs) {
		waitDuration := time.Duration(ledger.DiffTimeTicks(prevMilestoneTs, nowis)) * ledger.TickDuration()
		seq.log.Warnf("nowis (%s) is before last milestone ts (%s). Sleep %v",
			nowis.String(), prevMilestoneTs.String(), waitDuration)
		time.Sleep(waitDuration)
	}
	nowis = ledger.LogicalTimeNow()
	for ; nowis.Before(prevMilestoneTs); nowis = ledger.LogicalTimeNow() {
		seq.log.Warnf("nowis (%s) is before last milestone ts (%s). Sleep %v",
			nowis.String(), prevMilestoneTs.String(), sleepWaitingCurrentMilestoneTime)
		time.Sleep(sleepWaitingCurrentMilestoneTime)
	}
	// logical time now is approximately equal to the clock time
	nowis = ledger.LogicalTimeNow()
	util.Assertf(!nowis.Before(prevMilestoneTs), "!core.LogicalTimeNow().Before(prevMilestoneTs)")

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
func (seq *Sequencer) generateNextMilestoneTxForTargetTime(targetTs ledger.LogicalTime) *transaction.Transaction {
	seq.Tracef("seq", "generateNextMilestoneTxForTargetTime %s", targetTs)

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

func (seq *Sequencer) submitMilestone(tx *transaction.Transaction) *vertex.WrappedTx {
	panic("not implemented")
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
