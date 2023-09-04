package sequencer

import (
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/state"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/workflow"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	Sequencer struct {
		par                  Params
		log                  *zap.SugaredLogger
		factory              *milestoneFactory
		exit                 atomic.Bool
		stopWG               sync.WaitGroup
		stopOnce             sync.Once
		onMilestoneSubmitted func(seq *Sequencer, vid *utangle.WrappedTx)
		infoMutex            sync.RWMutex
		info                 Info
		traceNAhead          atomic.Int64
	}

	Params struct {
		SequencerName string
		Glb           *workflow.Workflow
		ChainID       core.ChainID
		ControllerKey ed25519.PrivateKey
		Pace          int // pace in slots
		LogLevel      zapcore.Level
		MaxFeeInputs  int
		MaxTargetTs   core.LogicalTime
		MaxMilestones int
		MaxBranches   int
		// ProvideStartOutputs explicitly returns sequencer and stem outputs where to start chain
		ProvideStartOutputs func() (utangle.WrappedOutput, utangle.WrappedOutput, error)
		// ProvideBootstrapSequencers returns list of sequencerIDs and fee amount where to send fees (bribe) for faster bootup of the sequencer
		ProvideBootstrapSequencers func() ([]core.ChainID, uint64)
	}

	Info struct {
		MsCounter              int
		BranchCounter          int
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
	PaceMinimumSlots = 5
)

var traceAll atomic.Bool

func SetTraceAll(value bool) {
	traceAll.Store(value)
}

func StartNew(par Params) (*Sequencer, error) {
	var err error

	name := par.SequencerName
	if name == "" {
		name = "seq"
	}
	name = fmt.Sprintf("[%s-%s]", name, par.ChainID.VeryShort())
	ret := &Sequencer{
		par: par,
	}
	ret.onMilestoneSubmitted = func(seq *Sequencer, vid *utangle.WrappedTx) {
		seq.LogMilestoneSubmitDefault(vid)
	}
	ret.log = testutil.NewNamedLogger(name, par.LogLevel)
	if int64(ret.par.Pace) < PaceMinimumSlots {
		ret.par.Pace = PaceMinimumSlots
		ret.log.Infof("sequencer pace has been adjusted to minimum value %d", PaceMinimumSlots)
	} else {
		ret.log.Infof("sequencer pace is %d time slots (%v)", ret.par.Pace, time.Duration(ret.par.Pace)*core.TransactionTimePaceDuration())
	}
	util.Assertf(par.MaxFeeInputs <= 254, "par.MaxFeeInputs <=254")
	ret.log.Debugf("sequencer starting..")
	ret.factory, err = createMilestoneFactory(par)
	if err != nil {
		return nil, err
	}
	ret.stopWG.Add(1)
	go ret.mainLoop()
	ret.log.Info("sequencer started")
	return ret, nil
}

func (seq *Sequencer) setTraceAhead(n int64) {
	seq.traceNAhead.Store(n)
}

func (seq *Sequencer) OnMilestoneSubmitted(fun func(seq *Sequencer, vid *utangle.WrappedTx)) {
	seq.onMilestoneSubmitted = fun
}

func (seq *Sequencer) OnMilestoneTransactionSubmitted(fun func(seq *Sequencer, tx *state.Transaction)) {
	seq.onMilestoneSubmitted = func(seq *Sequencer, vid *utangle.WrappedTx) {
		vid.Unwrap(utangle.UnwrapOptions{Vertex: func(v *utangle.Vertex) {
			fun(seq, v.Tx)
		}})
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
	if toNextBoundary <= (3*seq.par.Pace)/2 && toNextBoundary >= core.TransactionTimePaceInTicks {
		target = nowis.NextTimeSlotBoundary()
		return target
	}

	target = core.MaxLogicalTime(nowis, currentMilestoneTs).AddTimeTicks(seq.par.Pace) // preliminary target ts
	if nowis.TimeSlot() == target.TimeSlot() {
		// same slot
		if !core.ValidTimePace(target, target.NextTimeSlotBoundary()) {
			// too close to the epoch boundary -> issue branch transaction
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
		// if it is valid ts next epoch boundary, do the branch transaction
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

	var currentEpoch core.TimeSlot

	for !seq.exit.Load() {
		if seq.par.MaxMilestones != 0 && milestoneCount >= seq.par.MaxMilestones {
			seq.log.Infof("reached max limit of milestones %d -> stopping", seq.par.MaxMilestones)
			go seq.Stop()
			break
		}
		if seq.par.MaxBranches != 0 && branchCount >= seq.par.MaxBranches {
			seq.log.Infof("reached max limit of branch milestones %d -> stopping", seq.par.MaxBranches)
			go seq.Stop()
			break
		}

		targetTs := seq.chooseNextMilestoneTargetTime()

		if seq.par.MaxTargetTs != core.NilLogicalTime && targetTs.After(seq.par.MaxTargetTs) {
			seq.log.Infof("next target ts %s is after maximum ts %s -> stopping", targetTs, seq.par.MaxTargetTs)
			go seq.Stop()
			break
		}

		if currentEpoch != targetTs.TimeSlot() {
			currentEpoch = targetTs.TimeSlot()
			seq.log.Infof("TIME SLOT %d", currentEpoch)
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
		seq.onMilestoneSubmitted(seq, msOutput.VID)
	}
	seq.stopWG.Done()
}

// submitTransaction submits transaction to the workflow and waits for deterministic status: either added to the tangle or rejected
// The temporary VID of the transaction is replaced with the real one upon submission
func (seq *Sequencer) submitTransaction(tmpMsOutput utangle.WrappedOutput) *utangle.WrappedOutput {
	tx := tmpMsOutput.VID.UnwrapTransaction()
	util.Assertf(tx != nil, "tx != nil")

	retVID, err := seq.par.Glb.TransactionInWaitAppendSync(tx.Bytes(), true)
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
