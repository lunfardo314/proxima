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

		log                  *zap.SugaredLogger
		factory              *milestoneFactory
		exit                 atomic.Bool
		stopWG               sync.WaitGroup
		stopOnce             sync.Once
		onMilestoneSubmitted func(seq *Sequencer, vid *utangle.WrappedOutput)
		infoMutex            sync.RWMutex
		info                 Info
		traceNAhead          atomic.Int64
	}

	Info struct {
		In                     int
		Out                    int
		NumConsumedFeeOutputs  int
		NumFeeOutputsInMempool int
		NumOtherMsInMempool    int
		LedgerCoverage         uint64
		PrevLedgerCoverage     uint64
	}
)

const (
	PaceMinimumTicks    = 5
	DefaultMaxFeeInputs = 50
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
	}
	ret.log.Infof("sequencer pace is %d time slots (%v)",
		ret.config.Pace, time.Duration(ret.config.Pace)*core.TransactionTimePaceDuration())

	ret.log.Debugf("sequencer starting..")
	if err = ret.createMilestoneFactory(); err != nil {
		return nil, err
	}
	ret.stopWG.Add(1)

	go ret.mainLoop()

	lms := ret.factory.getLastMilestone()
	ret.log.Infof("sequencer has been started at %s (loglevel=%s)",
		lms.IDShort(), ret.log.Level().String())
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
	pace := subViper.GetInt(name + ".pace")
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

const minimumTagAlongFee = 100

func getGlobalTagAlongOption(chainID core.ChainID) (func() ([]core.ChainID, uint64), error) {
	if !viper.GetBool("tag_along_sequencer.enabled") {
		return nil, nil
	}
	chainIDToTagAlong, err := core.ChainIDFromHexString(viper.GetString("tag_along_sequencer.sequencer_id"))
	if err != nil {
		return nil, fmt.Errorf("can't parse tag-along sequencer ID: %v", err)
	}
	if chainID == chainIDToTagAlong {
		// no need to tag along to itself
		return nil, nil
	}
	fee := viper.GetUint64("tag_along_sequencer.fee")
	if fee < minimumTagAlongFee {
		fee = minimumTagAlongFee
	}

	return func() ([]core.ChainID, uint64) {
		return []core.ChainID{chainIDToTagAlong}, fee
	}, nil
}

func (seq *Sequencer) ID() *core.ChainID {
	ret := seq.factory.tipPool.chainID
	return &ret
}

func (seq *Sequencer) setTraceAhead(n int64) {
	seq.traceNAhead.Store(n)
}

func (seq *Sequencer) OnMilestoneSubmitted(fun func(seq *Sequencer, vid *utangle.WrappedOutput)) {
	seq.onMilestoneSubmitted = fun
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

func (seq *Sequencer) setLastMilestone(msOutput utangle.WrappedOutput) {
	seq.factory.setLastMilestone(msOutput)
	seq.updateInfo(msOutput)
}

const sleepWaitingCurrentMilestoneTime = 10 * time.Millisecond

func (seq *Sequencer) chooseNextMilestoneTargetTime() core.LogicalTime {
	currentMs := seq.factory.getLastMilestone()
	currentMilestoneTs := currentMs.Timestamp()

	nowis := core.LogicalTimeNow()
	if nowis.Before(currentMilestoneTs) {
		waitDuration := time.Duration(core.DiffTimeTicks(currentMilestoneTs, nowis)) * core.TimeTickDuration()
		seq.log.Warnf("nowis (%s) is before last milestone ts (%s). Sleep %v",
			nowis.String(), currentMilestoneTs.String(), waitDuration)
		time.Sleep(waitDuration)
	}
	nowis = core.LogicalTimeNow()
	for ; nowis.Before(currentMilestoneTs); nowis = core.LogicalTimeNow() {
		seq.log.Warnf("nowis (%s) is before last milestone ts (%s). Sleep %v",
			nowis.String(), currentMilestoneTs.String(), sleepWaitingCurrentMilestoneTime)
		time.Sleep(sleepWaitingCurrentMilestoneTime)
	}

	toNextBoundary := nowis.TimesTicksToNextSlotBoundary()
	seq.trace("chooseNextMilestoneTargetTime: latestMilestone: %s, nowis: %s", currentMs.IDShort(), nowis)

	var target core.LogicalTime
	if toNextBoundary <= (3*seq.config.Pace)/2 && toNextBoundary >= core.TransactionTimePaceInTicks {
		target = nowis.NextTimeSlotBoundary()
		return target
	}

	target = core.MaxLogicalTime(nowis, currentMilestoneTs).AddTimeTicks(seq.config.Pace) // preliminary target ts
	if nowis.TimeSlot() == target.TimeSlot() {
		// same slot
		if !core.ValidTimePace(target, target.NextTimeSlotBoundary()) {
			// too close to the slot boundary -> issue branch transaction
			target = target.NextTimeSlotBoundary()
			return target
		}
		return target
	}
	// jumping over slot boundary
	if target.TimeTick() == 0 {
		// right on the slot boundary
		return target
	}
	if core.ValidTimePace(currentMilestoneTs, currentMilestoneTs.NextTimeSlotBoundary()) {
		// if it is valid ts next slot boundary, do the branch transaction
		target = currentMilestoneTs.NextTimeSlotBoundary()
		return target
	}
	return target
}

// Returns nil if fails to generate acceptable bestSoFar until the deadline
func (seq *Sequencer) generateNextMilestoneForTargetTime(targetTs core.LogicalTime) *utangle.WrappedOutput {
	seq.trace("generateNextMilestoneForTargetTime %s", targetTs)
	absoluteDeadline := targetTs.Time().Add(10 * time.Millisecond)
	for {
		if time.Now().After(absoluteDeadline) {
			// too late, was too slow, failed to meet the target deadline
			return nil
		}

		msOutput := seq.factory.startProposingForTargetLogicalTime(targetTs)

		if !time.Now().After(absoluteDeadline) && msOutput != nil {
			util.Assertf(msOutput.Timestamp() == targetTs, "msOutput.output.Timestamp() (%v) == targetTs (%v)",
				msOutput.Timestamp(), targetTs)
			return msOutput
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (seq *Sequencer) mainLoop() {
	milestoneCount := 0
	branchCount := 0

	var currentTimeSlot core.TimeSlot

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

		targetTs := seq.chooseNextMilestoneTargetTime()

		if seq.config.MaxTargetTs != core.NilLogicalTime && targetTs.After(seq.config.MaxTargetTs) {
			seq.log.Infof("next target ts %s is after maximum ts %s -> stopping", targetTs, seq.config.MaxTargetTs)
			go seq.Stop()
			break
		}

		if currentTimeSlot != targetTs.TimeSlot() {
			currentTimeSlot = targetTs.TimeSlot()
			//seq.log.Infof("TIME SLOT %d", currentTimeSlot)
		}

		seq.trace("target ts: %s. Now is: %s", targetTs, core.LogicalTimeNow())

		tmpMsOutput := seq.generateNextMilestoneForTargetTime(targetTs)
		if tmpMsOutput == nil {
			// failed to generate transaction for target time. Start over with new target time
			time.Sleep(10 * time.Millisecond)
			continue
		}
		seq.log.Debugf("produced milestone %s for the target logical time %s", tmpMsOutput.IDShort(), targetTs)
		msOutput := seq.submitTransaction(*tmpMsOutput)
		if msOutput == nil {
			seq.log.Warnf("failed to submit milestone %d -- %s", milestoneCount+1, tmpMsOutput.IDShort())
			continue
		}
		milestoneCount++
		if msOutput.VID.IsBranchTransaction() {
			branchCount++
		}
		seq.setLastMilestone(*msOutput)
		seq.onMilestoneSubmitted(seq, msOutput)
	}
	seq.stopWG.Done()
}

// submitTransaction submits transaction to the workflow and waits for deterministic status: either added to the tangle or rejected
// The temporary VID of the transaction is replaced with the real one upon submission
func (seq *Sequencer) submitTransaction(tmpMsOutput utangle.WrappedOutput) *utangle.WrappedOutput {
	tx := tmpMsOutput.VID.UnwrapTransaction()
	util.Assertf(tx != nil, "tx != nil")

	retVID, err := seq.glb.TransactionInWaitAppendSync(tx.Bytes(), true)
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
