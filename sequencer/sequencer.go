package sequencer

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/workflow"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/viper"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	Sequencer struct {
		glb           *workflow.Workflow
		chainID       core.ChainID
		controllerKey ed25519.PrivateKey
		config        ConfigOptions

		log      *zap.SugaredLogger
		factory  *milestoneFactory
		exit     atomic.Bool
		stopWG   sync.WaitGroup
		stopOnce sync.Once

		onMilestoneSubmittedMutex sync.RWMutex
		onMilestoneSubmitted      func(seq *Sequencer, vid *utangle.WrappedOutput)

		infoMutex      sync.RWMutex
		info           Info
		traceNAhead    atomic.Int64
		prevTimeTarget core.LogicalTime
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
		AvgProposalDuration    time.Duration
		NumProposals           int
	}
)

const (
	PaceMinimumTicks    = 5
	DefaultMaxFeeInputs = 20
)

var traceAll atomic.Bool

func SetTraceAll(value bool) {
	traceAll.Store(value)
}

func defaultConfigOptions() ConfigOptions {
	return ConfigOptions{
		SequencerName: "seq",
		Pace:          PaceMinimumTicks,
		LogLevel:      zap.InfoLevel,
		LogOutputs:    []string{"stdout"},
		LogTimeLayout: general.TimeLayoutDefault,
		MaxFeeInputs:  DefaultMaxFeeInputs,
		MaxTargetTs:   core.NilLogicalTime,
		MaxMilestones: math.MaxInt,
		MaxBranches:   math.MaxInt,
	}
}

func StartNew(glb *workflow.Workflow, seqID core.ChainID, controllerKey ed25519.PrivateKey, opts ...ConfigOpt) (*Sequencer, error) {
	var err error

	cfg := defaultConfigOptions()
	for _, opt := range opts {
		opt(&cfg)
	}
	logName := fmt.Sprintf("[%s-%s]", cfg.SequencerName, seqID.VeryShort())

	ret := &Sequencer{
		glb:           glb,
		chainID:       seqID,
		controllerKey: controllerKey,
		config:        cfg,
		log:           general.NewLogger(logName, cfg.LogLevel, cfg.LogOutputs, cfg.LogTimeLayout),
	}

	ret.onMilestoneSubmitted = func(seq *Sequencer, wOut *utangle.WrappedOutput) {
		seq.LogMilestoneSubmitDefault(wOut)
		seq.LogStats()
	}
	ret.log.Infof("sequencer pace is %d time slots (%v)",
		ret.config.Pace, time.Duration(ret.config.Pace)*core.TransactionTimePaceDuration())

	ret.log.Debugf("sequencer starting..")
	if err = ret.createMilestoneFactory(); err != nil {
		return nil, err
	}
	ret.stopWG.Add(1)

	util.RunWrappedRoutine(cfg.SequencerName+"[mainLoop]", func() {
		ret.mainLoop()
	}, common.ErrDBUnavailable)

	ret.log.Infof("sequencer has been started (loglevel=%s)", ret.log.Level().String())
	return ret, nil
}

func StartFromConfig(glb *workflow.Workflow, name string) (*Sequencer, error) {
	subViper := viper.Sub("sequencers." + name)
	if subViper == nil {
		return nil, fmt.Errorf("can't read config")
	}

	if !subViper.GetBool("enable") {
		return nil, nil
	}
	seqID, err := core.ChainIDFromHexString(subViper.GetString("sequencer_id"))
	if err != nil {
		return nil, fmt.Errorf("StartFromConfig: can't parse sequencer ID: %v", err)
	}
	controllerKey, err := util.ED25519PrivateKeyFromHexString(subViper.GetString("controller_key"))
	if err != nil {
		return nil, fmt.Errorf("StartFromConfig: can't parse private key: %v", err)
	}
	pace := subViper.GetInt("pace")
	if pace < PaceMinimumTicks {
		pace = PaceMinimumTicks
	}

	maxFeeInputs := subViper.GetInt("max_fee_inputs")
	if maxFeeInputs < 1 {
		maxFeeInputs = 1
	}
	if maxFeeInputs > 254 {
		maxFeeInputs = 254
	}

	maxBranches := subViper.GetInt("max_branches")
	maxMilestones := subViper.GetInt("max_milestones")

	opts := []ConfigOpt{
		WithName(name),
		WithLogLevel(parseLogLevel(glb, subViper)),
		WithLogOutput(viper.GetString("logger.output")),
		WithPace(pace),
		WithMaxFeeInputs(maxFeeInputs),
		WithMaxBranches(maxBranches),
		WithMaxMilestones(maxMilestones),
		WithTraceTippool(subViper.GetBool("trace_tippool")),
	}

	return StartNew(glb, seqID, controllerKey, opts...)
}

func parseLogLevel(glb *workflow.Workflow, subViper *viper.Viper) zapcore.Level {
	lvl, err := zapcore.ParseLevel(subViper.GetString("loglevel"))
	if err == nil {
		return lvl
	}
	return glb.LogLevel()
}

func (seq *Sequencer) ID() *core.ChainID {
	ret := seq.factory.tipPool.chainID
	return &ret
}

func (seq *Sequencer) setTraceAhead(n int64) {
	seq.traceNAhead.Store(n)
}

func (seq *Sequencer) OnMilestoneSubmitted(fun func(seq *Sequencer, msOutput *utangle.WrappedOutput)) {
	seq.onMilestoneSubmittedMutex.Lock()
	defer seq.onMilestoneSubmittedMutex.Unlock()

	if seq.onMilestoneSubmitted == nil {
		seq.onMilestoneSubmitted = fun
	} else {
		prevFun := seq.onMilestoneSubmitted
		seq.onMilestoneSubmitted = func(seq *Sequencer, msOutput *utangle.WrappedOutput) {
			prevFun(seq, msOutput)
			fun(seq, msOutput)
		}
	}
}

func (seq *Sequencer) RunOnMilestoneSubmitted(wOut *utangle.WrappedOutput) {
	seq.onMilestoneSubmittedMutex.RLock()
	defer seq.onMilestoneSubmittedMutex.RUnlock()

	if seq.onMilestoneSubmitted != nil {
		seq.onMilestoneSubmitted(seq, wOut)
	}
}

func (seq *Sequencer) trace(format string, args ...any) {
	forceTrace := seq.traceNAhead.Dec() >= 0
	if forceTrace || traceAll.Load() {
		seq.log.Infof("TRACE "+format, args...)
	}
}

func (seq *Sequencer) forceTrace(format string, args ...any) {
	seq.setTraceAhead(1)
	seq.trace(format, args...)
}

func (seq *Sequencer) Stop() {
	seq.stopOnce.Do(func() {
		seq.log.Debug("sequencer stopping..")
		seq.exit.Store(true)
		seq.WaitStop()
		seq.log.Info("sequencer stopped")
	})
}

func (seq *Sequencer) WaitStop() {
	seq.stopWG.Wait()
}

const sleepWaitingCurrentMilestoneTime = 10 * time.Millisecond

func (seq *Sequencer) chooseNextTargetTime(avgProposalDuration time.Duration) core.LogicalTime {
	var prevMilestoneTs core.LogicalTime

	if currentMs := seq.factory.getLatestMilestone(); currentMs.VID != nil {
		prevMilestoneTs = currentMs.Timestamp()
	} else {
		seqOut, stemOut, found := seq.factory.utangle.GetSequencerBootstrapOutputs(seq.factory.tipPool.chainID)
		util.Assertf(found, "GetSequencerBootstrapOutputs failed")

		prevMilestoneTs = core.MaxLogicalTime(seqOut.Timestamp(), stemOut.Timestamp())
	}

	// synchronize clock
	nowis := core.LogicalTimeNow()
	if nowis.Before(prevMilestoneTs) {
		waitDuration := time.Duration(core.DiffTimeTicks(prevMilestoneTs, nowis)) * core.TimeTickDuration()
		seq.log.Warnf("nowis (%s) is before last milestone ts (%s). Sleep %v",
			nowis.String(), prevMilestoneTs.String(), waitDuration)
		time.Sleep(waitDuration)
	}
	nowis = core.LogicalTimeNow()
	for ; nowis.Before(prevMilestoneTs); nowis = core.LogicalTimeNow() {
		seq.log.Warnf("nowis (%s) is before last milestone ts (%s). Sleep %v",
			nowis.String(), prevMilestoneTs.String(), sleepWaitingCurrentMilestoneTime)
		time.Sleep(sleepWaitingCurrentMilestoneTime)
	}
	// TODO taking into account average speed of proposal generation

	nowis = core.LogicalTimeNow()
	util.Assertf(!nowis.Before(prevMilestoneTs), "!core.LogicalTimeNow().Before(prevMilestoneTs)")

	targetAbsoluteMinimum := prevMilestoneTs.AddTimeTicks(seq.config.Pace)
	nextSlotBoundary := nowis.NextTimeSlotBoundary()

	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum
	}
	// absolute minimum is before the next slot boundary
	// set absolute minimum starting from now
	minimumTicksAheadFromNow := (seq.config.Pace * 2) / 3 // seq.config.Pace
	targetAbsoluteMinimum = nowis.AddTimeTicks(minimumTicksAheadFromNow)
	if !targetAbsoluteMinimum.Before(nextSlotBoundary) {
		return targetAbsoluteMinimum
	}

	if targetAbsoluteMinimum.TimesTicksToNextSlotBoundary() <= seq.config.Pace {
		return nextSlotBoundary
	}

	return targetAbsoluteMinimum
}

// Returns nil if fails to generate acceptable bestSoFarTx until the deadline
func (seq *Sequencer) generateNextMilestoneForTargetTime(targetTs core.LogicalTime) (*transaction.Transaction, time.Duration, int) {
	seq.trace("generateNextMilestoneForTargetTime %s", targetTs)

	timeout := time.Duration(seq.config.Pace) * core.TimeTickDuration()
	absoluteDeadline := targetTs.Time().Add(timeout)

	if absoluteDeadline.Before(time.Now()) {
		// too late, was too slow, failed to meet the target deadline
		seq.log.Warnf("didn't start proposers for target %s: nowis %v, too late for absolute deadline %v",
			targetTs.String(), time.Now(), absoluteDeadline)
		return nil, 0, 0
	}

	ms, avgProposalDuration, numProposals := seq.factory.startProposingForTargetLogicalTime(targetTs)

	if ms != nil {
		util.Assertf(ms.Timestamp() == targetTs, "msOutput.output.Timestamp() (%v) == targetTs (%v)",
			ms.Timestamp(), targetTs)
		return ms, avgProposalDuration, numProposals
	}
	return ms, avgProposalDuration, numProposals
}

func (seq *Sequencer) mainLoop() {
	milestoneCount := 0
	branchCount := 0

	var currentTimeSlot core.TimeSlot
	var avgProposalDuration time.Duration

	for !seq.exit.Load() {
		if seq.config.MaxMilestones != 0 && milestoneCount >= seq.config.MaxMilestones {
			seq.log.Infof("reached max limit of milestones %d -> stopping", seq.config.MaxMilestones)
			go seq.Stop()
			break
		}
		if seq.config.MaxBranches != 0 && branchCount >= seq.config.MaxBranches {
			seq.log.Infof("reached max limit of branch milestones %d -> stopping", seq.config.MaxBranches)
			go seq.Stop()
			break
		}

		timerStart := time.Now()

		targetTs := seq.chooseNextTargetTime(avgProposalDuration)
		util.Assertf(!targetTs.Before(seq.prevTimeTarget), "wrong target ts %s: must not be before previous target %s",
			targetTs.String(), seq.prevTimeTarget.String())
		seq.prevTimeTarget = targetTs

		if seq.config.MaxTargetTs != core.NilLogicalTime && targetTs.After(seq.config.MaxTargetTs) {
			seq.log.Infof("next target ts %s is after maximum ts %s -> stopping", targetTs, seq.config.MaxTargetTs)
			go seq.Stop()
			break
		}

		if currentTimeSlot != targetTs.TimeSlot() {
			currentTimeSlot = targetTs.TimeSlot()
		}

		//seq.setTraceAhead(1)
		seq.trace("target ts: %s. Now is: %s", targetTs, core.LogicalTimeNow())

		ms, avgProposalDuration, numProposals := seq.generateNextMilestoneForTargetTime(targetTs)
		if ms == nil {
			//seq.setTraceAhead(1)
			seq.trace("failed to generate ms for target: %s. Now is: %s", targetTs, core.LogicalTimeNow())
			time.Sleep(10 * time.Millisecond)
			continue
		}

		//seq.setTraceAhead(1)
		seq.trace("produced milestone %s for the target logical time %s in %v, avg proposal: %v",
			ms.IDShort(), targetTs, time.Since(timerStart), avgProposalDuration)

		msOutput := seq.submitMilestone(ms)

		if global.IsShuttingDown() {
			// maybe todo something better
			go seq.Stop()
		}

		if msOutput == nil {
			continue
		}
		seq.factory.addOwnMilestone(*msOutput)

		milestoneCount++
		if msOutput.VID.IsBranchTransaction() {
			branchCount++
		}
		seq.updateInfo(*msOutput, avgProposalDuration, numProposals)
		seq.RunOnMilestoneSubmitted(msOutput)
	}
	seq.stopWG.Done()
}

const submitTransactionTimeout = 5 * time.Second

// submitMilestone submits transaction to the workflow and waits for deterministic status: either added to the tangle or rejected
// The temporary VID of the transaction is replaced with the real one upon submission
func (seq *Sequencer) submitMilestone(tx *transaction.Transaction) *utangle.WrappedOutput {
	util.Assertf(tx != nil, "tx != nil")

	retVID, err := seq.glb.TransactionInWaitAppendWrap(tx.Bytes(), submitTransactionTimeout, workflow.OptionWithSourceSequencer)
	if global.IsShuttingDown() {
		return nil
	}
	if err != nil {
		seq.log.Warnf("submitMilestone: %v", err)
		seq.log.Errorf("====================== failed milestone ==================\n%s", tx.ToString(seq.factory.utangle.GetUTXO))
		seq.factory.utangle.SaveGraph("submit_milestone_fail")
		return nil
	}
	seq.log.Debugf("submited milestone:: %s", tx.IDShort())
	return &utangle.WrappedOutput{
		VID:   retVID,
		Index: tx.SequencerTransactionData().SequencerOutputIndex,
	}
}

func (seq *Sequencer) NumOutputsInPool() int {
	return seq.factory.tipPool.numOutputsInBuffer()
}
