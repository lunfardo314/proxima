package sequencer

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/workflow"
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

	go ret.mainLoop()

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

func (seq *Sequencer) OnMilestoneSubmitted(fun func(seq *Sequencer, vid *utangle.WrappedOutput)) {
	seq.onMilestoneSubmittedMutex.Lock()
	defer seq.onMilestoneSubmittedMutex.Unlock()

	if seq.onMilestoneSubmitted == nil {
		seq.onMilestoneSubmitted = fun
	} else {
		prevFun := seq.onMilestoneSubmitted
		seq.onMilestoneSubmitted = func(seq *Sequencer, vid *utangle.WrappedOutput) {
			prevFun(seq, vid)
			fun(seq, vid)
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
	var target core.LogicalTime
	var prevMilestoneTs core.LogicalTime

	if currentMs := seq.factory.getLatestMilestone(); currentMs.VID != nil {
		prevMilestoneTs = currentMs.Timestamp()
	} else {
		seqOut, stemOut, found := seq.factory.tangle.GetSequencerBootstrapOutputs(seq.factory.tipPool.chainID)
		util.Assertf(found, "GetSequencerBootstrapOutputs failed")

		prevMilestoneTs = core.MaxLogicalTime(seqOut.Timestamp(), stemOut.Timestamp())
	}

	nowis := core.LogicalTimeNow()
	// synchronize clock
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

	const howManyAvgProposals = 10
	timeTicksForSomeProposals := int((howManyAvgProposals * avgProposalDuration) / core.TimeTickDuration())
	nowis = core.LogicalTimeNow()
	// earliest reasonable. In next steps will move target further, if necessary
	target = core.MaxLogicalTime(prevMilestoneTs.AddTimeTicks(seq.config.Pace), nowis.AddTimeTicks(timeTicksForSomeProposals))

	seq.trace("chooseNextTargetTime: nowis: %s, preliminary target: %s", nowis.String(), target.String())

	if seq.prevTimeTarget == core.NilLogicalTime {
		// it is first milestone, go right to branch generation
		target = target.NextTimeSlotBoundary()
		return target
	}

	if target.TimesTicksToNextSlotBoundary() <= seq.config.Pace {
		// it is too close to the boundary
		target = target.NextTimeSlotBoundary()
		return target
	}

	if target.TimeTick() == 0 {
		// right on the slot boundary
		return target
	}
	return target
}

// Returns nil if fails to generate acceptable bestSoFar until the deadline
func (seq *Sequencer) generateNextMilestoneForTargetTime(targetTs core.LogicalTime) (*utangle.WrappedOutput, time.Duration, int) {
	seq.trace("generateNextMilestoneForTargetTime %s", targetTs)

	timeout := time.Duration(seq.config.Pace) * core.TimeTickDuration()
	absoluteDeadline := targetTs.Time().Add(timeout)

	for {
		if time.Now().After(absoluteDeadline) {
			// too late, was too slow, failed to meet the target deadline
			return nil, 0, 0
		}

		msOutput, avgProposalDuration, numProposals := seq.factory.startProposingForTargetLogicalTime(targetTs)

		if msOutput != nil {
			util.Assertf(msOutput.Timestamp() == targetTs, "msOutput.output.Timestamp() (%v) == targetTs (%v)",
				msOutput.Timestamp(), targetTs)
			return msOutput, avgProposalDuration, numProposals
		}

		if time.Now().After(absoluteDeadline) {
			// too late, was too slow, failed to meet the target deadline
			seq.log.Warnf("proposal is late for the time target %s. Avg proposal duration %v, num proposals: %d",
				targetTs.String(), avgProposalDuration, numProposals)
			return nil, avgProposalDuration, numProposals
		}
		time.Sleep(10 * time.Millisecond)
	}
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
		util.Assertf(targetTs.After(seq.prevTimeTarget), "target (%s) must be after seq.prevTimeTarget (%s)",
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

		var tmpMsOutput *utangle.WrappedOutput
		var numProposals int
		tmpMsOutput, avgProposalDuration, numProposals = seq.generateNextMilestoneForTargetTime(targetTs)
		if tmpMsOutput == nil {
			// failed to generate transaction for target time. Start over with new target time
			time.Sleep(10 * time.Millisecond)
			continue
		}

		//seq.setTraceAhead(1)
		seq.trace("produced milestone %s for the target logical time %s in %v, avg proposal: %v",
			tmpMsOutput.IDShort(), targetTs, time.Since(timerStart), avgProposalDuration)

		msOutput := seq.submitTransaction(*tmpMsOutput)
		if msOutput == nil {
			seq.log.Warnf("failed to submit milestone %d -- %s", milestoneCount+1, tmpMsOutput.IDShort())
			util.Panicf("debug exit")
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

// submitTransaction submits transaction to the workflow and waits for deterministic status: either added to the tangle or rejected
// The temporary VID of the transaction is replaced with the real one upon submission
func (seq *Sequencer) submitTransaction(tmpMsOutput utangle.WrappedOutput) *utangle.WrappedOutput {
	tx := tmpMsOutput.VID.UnwrapTransaction()
	util.Assertf(tx != nil, "tx != nil")

	retVID, err := seq.glb.TransactionInWaitAppendWrap(tx.Bytes(), submitTransactionTimeout, workflow.OptionWithSourceSequencer)
	if err != nil {
		seq.log.Errorf("submitTransaction: %v", err)
		return nil
	}
	seq.log.Debugf("submited milestone:: %s", tx.IDShort())
	return &utangle.WrappedOutput{
		VID:   retVID,
		Index: tmpMsOutput.Index,
	}
}

func (seq *Sequencer) NumOutputsInPool() int {
	return seq.factory.tipPool.numOutputsInBuffer()
}
