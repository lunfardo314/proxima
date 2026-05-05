package sequencer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/backlog"
	"github.com/lunfardo314/proxima/sequencer/factory"
	"github.com/lunfardo314/proxima/sequencer/task"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/checkpoints"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	Environment interface {
		global.NodeGlobal
		attacher.Environment
		IsSynced() bool
		TxBytesStore() global.TxBytesStore
		GetLatestMilestone(seqID base.ChainID) *vertex.WrappedTx
		LatestMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		LatestMilestonesShuffled(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		NumSequencerTips() int
		ListenToControllerAccount(account ledger.Controller, fun func(wOut vertex.WrappedOutput))
		MustEnsureBranch(txid base.TransactionID) *vertex.WrappedTx
		OwnSequencerMilestoneIn(txBytes []byte, meta *txmetadata.TransactionMetadata, txid base.TransactionID)
		LatestReliableState() (multistate.SugaredStateReader, error)
	}

	Sequencer struct {
		Environment
		ctx                  context.Context    // local context
		stopFun              context.CancelFunc // local stop function
		sequencerID          base.ChainID
		controllerKey        ed25519.PrivateKey
		backlog              *backlog.TagAlongBacklog
		config               *ConfigOptions
		log                  *zap.SugaredLogger
		ownMilestonesMutex   sync.RWMutex
		ownMilestones        map[*vertex.WrappedTx]outputsWithTime // map ms -> consumed outputs in the past
		milestoneCount       int
		branchCount          int
		lastSubmittedTs      base.LedgerTime
		infoMutex            sync.RWMutex
		info                 Info
		onCallbackMutex      sync.RWMutex
		onMilestoneSubmitted func(seq *Sequencer, vid *vertex.WrappedTx)
		onExit               func()
		slotData             *task.SlotData
		wontSubmitBranchID   base.TransactionID
		metrics              *sequencerMetrics
		skeletonFactory      *factory.Factory
		// budgetLevel tracks the tag-along budget allowance (0..maxBudgetLevel).
		// Starts at max (full budget). Cuts sharply on failure, increases slowly on success.
		// TCP-like congestion control for tag-along throughput.
		budgetLevel int
		// pendingSubmit tracks the last milestone submitted via fire-and-forget that
		// hasn't yet appeared back in the tippool. Used to throttle the sequencer when
		// self-attachment latency exceeds tolerance, preventing the submit-faster-than-attach
		// spiral that detaches the sequencer from its own chain under heavy load.
		pendingSubmitMu     sync.Mutex
		pendingSubmit       pendingSubmitStatus
		lastOverloadLogSlot uint32
		// lastPulseAnchor anchors the sequencer pulse (see strategy_async.go).
		// Updated on: own-milestone tippool observation, successful or failed pulse attempt.
		// The pulse fires when (time.Since(lastPulseAnchor) >= pulseInterval) AND the
		// previous own milestone has been observed (pendingSubmit.awaiting == false).
		lastPulseAnchor time.Time
		// loopCheckpoint is the deadlock watchdog for the sequencer loop. Fed once per
		// tick from inside doSequencerSlot so the tolerance reflects loop liveness, not
		// slot-completion cadence (which varies under load when slots are skipped).
		loopCheckpoint *checkpoints.Checkpoints
	}

	pendingSubmitStatus struct {
		awaiting bool
		since    time.Time
		ts       base.LedgerTime
		txID     base.TransactionID
	}

	outputsWithTime struct {
		consumed set.Set[base.OutputID]
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

func New(env Environment, seqID base.ChainID, controllerKey ed25519.PrivateKey, opts ...ConfigOption) (*Sequencer, error) {
	cfg := configOptions(opts...)
	out := viper.GetString("logger.output") + ".seq"
	global.MaintainLogs(out, viper.GetString("logger.previous"), viper.GetInt("logger.keep_latest_logs"))

	displayName := cfg.SequencerName
	if displayName == "" {
		displayName = seqID.StringHex()[:4]
	}
	logName := "[SEQ:" + displayName + "]"
	var log *zap.SugaredLogger
	if cfg.SeparateLog {
		outputs := []string{out}
		if cfg.GlobalLogging {
			outputs = append(outputs, env.Outputs()...)
		}
		log = global.NewLogger(logName, zapcore.InfoLevel, outputs, global.TimeLayoutDefault)
	} else {
		log = env.Log().Named(logName)
	}
	log.Infof("starting sequencer '%s', seqID: %s", displayName, seqID.String())

	ret := &Sequencer{
		Environment:   env,
		sequencerID:   seqID,
		controllerKey: controllerKey,
		ownMilestones: make(map[*vertex.WrappedTx]outputsWithTime),
		config:        cfg,
		log:           log,
		budgetLevel:   maxBudgetLevel, // start at full budget
	}
	if cfg.SingleSequencerEnforced {
		ret.metrics = &sequencerMetrics{}
		ret.registerMetrics()
	}

	ret.ctx, ret.stopFun = context.WithCancel(env.Ctx())
	var err error

	if ret.backlog, err = backlog.New(ret); err != nil {
		return nil, err
	}
	if err = ret.backlog.LoadSequencerStartTips(seqID); err != nil {
		return nil, err
	}
	if controllerKey != nil {
		ret.Log().Infof("sequencer is starting with config:\n%s", cfg.lines(seqID,
			ledger.SigLockFromED25519PrivateKey(controllerKey), "     ").String())
	} else {
		ret.Log().Infof("sequencer created, controller key will be loaded during startup")
	}

	return ret, nil
}

func NewFromConfig(glb *workflow.Workflow) (*Sequencer, error) {
	cfg, seqID, err := paramsFromConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	return New(glb, seqID, nil, cfg...)
}

func (seq *Sequencer) Start() {
	runFun := func() {
		seq.MarkWorkProcessStarted(seq.config.SequencerName)
		defer seq.MarkWorkProcessStopped(seq.config.SequencerName)

		if !seq.ensurePreConditions() {
			return
		}

		seq.log.Infof("sequencer has been STARTED %s", util.Ref(seq.SequencerID()).String())

		ttl := time.Duration(seq.config.MilestonesTTLSlots) * ledger.L(0).SlotDuration()

		seq.RepeatInBackground(seq.SequencerName()+"_own_milestone_cleanup", ownMilestoneCleanupPeriod, func() bool {
			if n, remain := seq.purgeOwnMilestones(ttl); n > 0 {
				seq.Log().Infof("purged %d own milestones, %d remain. TTL = %v", n, remain, ttl)
			}
			return true
		}, true)

		seq.RepeatInBackground(seq.SequencerName()+"_own_milestone_recreate_map", ownMilestoneMapRecreatePeriod, func() bool {
			seq.recreateMapOwnMilestones()
			return true
		})

		// start the skeleton factory — runs as a persistent goroutine producing skeletons
		seq.skeletonFactory = factory.New(seq, seq.ctx)
		go seq.skeletonFactory.Run()

		// start the background milestone watcher
		go seq.milestoneWatcher()

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
			if seq.Ctx().Err() != nil {
				// panic during shutdown (e.g., ledger library cleaned up) — suppress
				seq.log.Warnf("sequencer loop panic during shutdown: %v", err)
				return false
			}
			seq.log.Fatal(err)
			return false
		})
	}
}

func (seq *Sequencer) ensureSyncedIfNecessary() bool {
	if !seq.config.EnsureSyncedBeforeStart {
		return true
	}
	seq.Log().Infof("ensureSyncedIfNecessary: ensure node is synced before starting sequencer...")
	seq.RepeatSync(2*time.Second, func() bool {
		seq.Log().Infof("ensureSyncedIfNecessary: waiting for node synced before starting sequencer...")
		return !seq.IsSynced()
	})
	return seq.IsSynced()
}

func (seq *Sequencer) ensureNotTooCloseToSnapshot() {
	snapshotSlot := seq.Branches().SnapshotSlot()
	if snapshotSlot == 0 {
		return
	}

	seq.RepeatSync(5*time.Second, func() bool {
		slotNow := ledger.TimeNow().Slot
		slotDiff := slotNow - snapshotSlot
		if slotDiff > 64 {
			return false
		}
		seq.log.Warnf("current slot %d must be >64 slots ahead from the snapshot slot %d. Waiting for another %d slots before starting the sequencer..",
			slotNow, snapshotSlot, 64-slotDiff+1)
		return true
	})
}

func (seq *Sequencer) ensurePreConditions() bool {
	if !seq.ensureControllerKey() {
		return false
	}

	if !seq.ensureSyncedIfNecessary() {
		seq.log.Warnf("ensurePreConditions: node is not synced. Can't start sequencer. EXIT..")
		return false
	}
	seq.log.Infof("ensurePreConditions: node is synced")

	seq.ensureNotTooCloseToSnapshot()

	snapshotID := seq.Branches().SnapshotBranchID()
	seq.log.Infof("ensurePreConditions: snapshot branch is %s", snapshotID.String())

	if !seq.ensureFirstMilestone() {
		seq.log.Warnf("ensurePreConditions: Can't start sequencer. EXIT..")
		return false
	}
	return true
}

// ensureControllerKey loads the controller private key from the keystore file if not already set.
// Supports unencrypted keystores and encrypted keystores with passphrase from SEQUENCER_KEY_PASSPHRASE env var.
// Returns false if the key cannot be loaded; sequencer must not start without a valid controller key.
func (seq *Sequencer) ensureControllerKey() bool {
	if seq.controllerKey != nil {
		// key was provided directly (e.g. in tests)
		return true
	}
	keyFile := seq.config.ControllerKeyFile
	if keyFile == "" {
		seq.log.Errorf("ensureControllerKey: controller key not available: set 'controller_key_file' in sequencer config. Sequencer will not start")
		return false
	}

	ks, err := keystore.LoadFromFile(keyFile)
	if err != nil {
		seq.log.Errorf("ensureControllerKey: failed to load keystore '%s': %v. Sequencer will not start", keyFile, err)
		return false
	}
	if ks.KeyType != keystore.KeyTypeED25519 {
		seq.log.Errorf("ensureControllerKey: unsupported key type %d in keystore '%s'. Sequencer will not start", ks.KeyType, keyFile)
		return false
	}

	passphrase := ""
	encrypted := ks.IsEncrypted()
	if encrypted {
		if p, ok := ks.ReadPassphraseFile(); ok {
			passphrase = p
		} else {
			passphrase = os.Getenv("SEQUENCER_KEY_PASSPHRASE")
		}
		if passphrase == "" {
			seq.log.Errorf("ensureControllerKey: keystore '%s' is encrypted: set SEQUENCER_KEY_PASSPHRASE environment variable or provide passphrase file. Sequencer will not start", keyFile)
			return false
		}
	}

	keyBytes, err := ks.GetPrivateKey(passphrase)
	passphrase = ""
	if err != nil {
		seq.log.Errorf("ensureControllerKey: failed to decrypt keystore '%s': %v. Sequencer will not start", keyFile, err)
		return false
	}
	if len(keyBytes) != ed25519.PrivateKeySize {
		seq.log.Errorf("ensureControllerKey: key in '%s' has wrong size %d (expected %d). Sequencer will not start", keyFile, len(keyBytes), ed25519.PrivateKeySize)
		return false
	}

	seq.controllerKey = keyBytes

	if encrypted {
		seq.log.Infof("ensureControllerKey: controller key loaded from encrypted keystore '%s' (passphrase from SEQUENCER_KEY_PASSPHRASE)", keyFile)
	} else {
		seq.log.Infof("ensureControllerKey: controller key loaded from unencrypted keystore '%s'", keyFile)
	}
	seq.log.Infof("sequencer config:\n%s", seq.config.lines(seq.sequencerID,
		ledger.SigLockFromED25519PrivateKey(seq.controllerKey), "     ").String())
	return true
}

const ensureStartingMilestoneTimeout = 5 * time.Second

// ensureFirstMilestone waiting for the first sequencer milestone arrive
func (seq *Sequencer) ensureFirstMilestone() bool {
	// First, verify the sequencer ID exists in the ledger state
	if !seq.validateSequencerIDExists() {
		return false
	}

	var startOutput vertex.WrappedOutput

	deadline := time.Now().Add(ensureStartingMilestoneTimeout)
	succ := seq.RepeatSync(ledger.L(0).TickDuration, func() bool {
		if time.Now().After(deadline) {
			return false
		}
		startOutput = seq.OwnLatestMilestoneOutput()
		return startOutput.VID == nil || !startOutput.IsAvailable()
	})

	if !succ {
		seq.log.Errorf("ensureFirstMilestone: interrupted")
		return false
	}
	if startOutput.VID == nil || !startOutput.IsAvailable() {
		seq.log.Errorf("failed to find chain output to start")
		return false
	}
	if !seq.checkSequencerStartOutput(startOutput) {
		return false
	}
	seq.AddOwnMilestone(startOutput.VID)

	if sleepDuration := time.Until(ledger.ClockTime(startOutput.Timestamp())); sleepDuration > 0 {
		seq.log.Warnf("will delay start for %v to sync ledger time with the clock", sleepDuration)
		if !seq.ClockCatchUpWithLedgerTime(startOutput.Timestamp()) {
			// interrupted by shutdown
			return false
		}
	}
	return true
}

func (seq *Sequencer) checkSequencerStartOutput(wOut vertex.WrappedOutput) bool {
	util.Assertf(wOut.VID != nil, "wOut.VID != nil")
	if !wOut.VID.IsSequencerTransaction() {
		seq.log.Warnf("checkSequencerStartOutput: start output %s is not a sequencer output", wOut.IDStringShort())
	}
	oReal, err := wOut.VID.OutputAt(wOut.Index)
	if oReal == nil || err != nil {
		seq.log.Errorf("checkSequencerStartOutput: failed to load start output %s: %s", wOut.IDStringShort(), err)
		return false
	}
	lock := oReal.Lock()
	expectedHolder := ledger.SigLockFromED25519PrivateKey(seq.controllerKey)
	// Match by the index-value tuple — any position whose bytes equal
	// the expected holder counts (e.g. delegate's master at position 0,
	// or sigLock's holder at position 0).
	expectedID := expectedHolder.ControllerID()
	matches := false
	for _, v := range oReal.IndexValues() {
		if bytes.Equal(v, expectedID) {
			matches = true
			break
		}
	}
	if !matches {
		seq.log.Errorf("checkSequencerStartOutput: provided private key does match sequencer lock %s", lock.String())
		return false
	}
	seq.log.Infof("checkSequencerStartOutput: sequencer controller is %s", lock.String())

	amount := oReal.TokenBalance()
	seq.log.Infof("sequencer start output %s has amount %s (%s%% of the initial supply)",
		wOut.IDStringShort(), util.Th(amount), util.PercentString(int(amount), int(ledger.L(0).InitialSupply)))
	return true
}

func (seq *Sequencer) Ctx() context.Context {
	return seq.ctx
}

func (seq *Sequencer) Stop() {
	seq.stopFun()
}

func (seq *Sequencer) Backlog() *backlog.TagAlongBacklog {
	return seq.backlog
}

func (seq *Sequencer) SkeletonFactory() *factory.Factory {
	return seq.skeletonFactory
}

func (seq *Sequencer) SequencerID() base.ChainID {
	return seq.sequencerID
}

func (seq *Sequencer) ControllerKeys() (byte, []byte, []byte) {
	return base.SignatureTypeED25519, seq.controllerKey, seq.controllerKey.Public().(ed25519.PublicKey)
}

func (seq *Sequencer) SequencerName() string {
	if seq.config.SequencerName == "" {
		return seq.sequencerID.StringHex()[:4]
	}
	return seq.config.SequencerName
}

// override global logger

func (seq *Sequencer) Log() *zap.SugaredLogger {
	return seq.log
}

func (seq *Sequencer) Tracef(tag string, format string, args ...any) {
	seq.TracefLog(seq.log, tag, format, args...)
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
	}()

	// On deadlock suspicion, dump all goroutines and initiate a graceful shutdown
	// (rather than Fatalf, which kills the process immediately and skips DB flush / peer
	// cleanup). The full stack dump is still logged at Error level so the cause can be
	// investigated post-mortem.
	seq.loopCheckpoint = checkpoints.New(func(name string) {
		buf := make([]byte, 4<<20) // 4MB buffer to capture all goroutines
		n := runtime.Stack(buf, true)
		seq.Log().Errorf(">>>>>>>> DEADLOCK suspected in the sequencer loop:\n%s", string(buf[:n]))
		seq.GracefulShutdown("deadlock suspected in sequencer loop")
	})
	defer seq.loopCheckpoint.Close()

	for {
		select {
		case <-seq.Ctx().Done():
			return
		default:
			if !seq.doSequencerSlot() {
				return
			}
		}

		// check if context was cancelled during the slot (shutdown race with ledger cleanup)
		if seq.Ctx().Err() != nil {
			return
		}
	}
}

// SeqLoopDeadlockTolerance bounds the time the inner per-tick loop in
// doSequencerSlot may go without making progress. Fed once per tick from inside
// strategy_async.go so this is the actual stuck-loop threshold, not a per-slot bound.
const SeqLoopDeadlockTolerance = 30 * time.Second

// checkLoopCheckpoint feeds the loop watchdog with a fresh deadline. Called per
// tick from doSequencerSlot. No-op if the watchdog hasn't been installed yet
// (e.g. in tests that drive doSequencerSlot directly).
func (seq *Sequencer) checkLoopCheckpoint() {
	if seq.loopCheckpoint != nil {
		seq.loopCheckpoint.Check("SEQ_LOOP", SeqLoopDeadlockTolerance)
	}
}

// cancelLoopCheckpoint clears the watchdog deadline so an intentional wait
// (snapshot pause, clock catch-up) doesn't trigger a false-positive deadlock.
// The next checkLoopCheckpoint call re-arms it.
func (seq *Sequencer) cancelLoopCheckpoint() {
	if seq.loopCheckpoint != nil {
		seq.loopCheckpoint.Check("SEQ_LOOP")
	}
}

const TraceTagTarget = "target"

const (
	// maxBudgetLevel is the maximum tag-along budget level.
	// Budget scales linearly: 0 = no tag-alongs, maxBudgetLevel = full 2/3 budget.
	maxBudgetLevel = 6
	// budgetCutOnFailure: on deadline exceeded / no proposals, cut budget by this amount.
	// Sharp decrease for fast response to overload.
	budgetCutOnFailure = 3
	// budgetIncreaseOnSuccess: on each successful milestone, increase budget by 1.
	// Slow ramp-up to probe how much load the sequencer can handle.
	budgetIncreaseOnSuccess = 1
)

// adjustBudget updates the tag-along budget level based on milestone outcome.
// TCP-like congestion control: slow increase on success, sharp cut on failure.
// No-op when throttling is disabled.
func (seq *Sequencer) adjustBudget(success bool) {
	if seq.config.DisableThrottle {
		return
	}
	if success {
		seq.budgetLevel += budgetIncreaseOnSuccess
		if seq.budgetLevel > maxBudgetLevel {
			seq.budgetLevel = maxBudgetLevel
		}
	} else {
		seq.budgetLevel -= budgetCutOnFailure
		if seq.budgetLevel < 0 {
			seq.budgetLevel = 0
		}
	}
}

// TagAlongBudgetNumerator returns the tag-along budget numerator scaled by budget level.
// At level 0: no tag-alongs. At maxBudgetLevel: full 2/3 budget (numerator=2, denominator=3).
// When ForceActivity is set, minimum numerator is 1 (1/3 budget).
// When DisableThrottle is set, always returns full budget (2).
// The denominator is always 3 (from tagAlongBudgetFraction).
func (seq *Sequencer) TagAlongBudgetNumerator() int {
	if seq.config.DisableThrottle {
		return 2
	}
	minNumerator := 0
	if seq.config.ForceActivity {
		minNumerator = 1
	}
	// linear scale: numerator = 2 * budgetLevel / maxBudgetLevel
	numerator := 2 * seq.budgetLevel / maxBudgetLevel
	if numerator < minNumerator {
		numerator = minNumerator
	}
	return numerator
}

func (seq *Sequencer) MaxTagAlongInputs() int {
	return seq.config.MaxTagAlongInputs
}

// decideSubmitMilestone checks health and connectivity before submitting a milestone.
// Aggregates (CoverageDelta / Supply / TotalCoverage / SlotInflation) come from
// the produced stem on branch txs (post metadata-refactor §7); for non-branch
// txs we read the proposer-computed ledger coverage from `proposed` (passed by
// the caller) since non-branch txs don't produce a stem.
func (seq *Sequencer) decideSubmitMilestone(tx *transaction.Transaction, ledgerCoverage uint64) bool {
	if !seq.IsConnectedToNetwork() {
		if seq.wontSubmitBranchID != tx.ID() {
			// prevent excess logging of the same message
			seq.Log().Warnf("WON'T SUBMIT BRANCH %s: node is disconnected from the network", tx.IDShortString())
			seq.wontSubmitBranchID = tx.ID()
			return false
		}
		return false
	}
	if tx.IsBranchTransaction() {
		if overloaded, elapsed, pending := seq.isOverloaded(); overloaded {
			if seq.wontSubmitBranchID != tx.ID() {
				tolerance := time.Duration(selfAttachmentLatencyToleranceTicks) * ledger.TickDuration()
				seq.Log().Warnf("WON'T SUBMIT BRANCH %s. reason: self-attachment latency %v exceeds tolerance %v (pending %s)",
					tx.IDShortString(), elapsed, tolerance, pending.txID.StringShort())
				seq.wontSubmitBranchID = tx.ID()
			}
			return false
		}
		// Read deterministic aggregates from the produced stem (§7).
		stemOut := tx.FindStemProducedOutput()
		stemLock, _ := stemOut.Output.StemLock()
		healthy := global.IsHealthyCoverageDelta(stemLock.CoverageDelta, stemLock.TotalSupply, global.FractionHealthyBranch())
		if healthy {
			sd := tx.SequencerTransactionData().SequencerOutputData.SequencerData
			seq.Log().Infof("SUBMIT BRANCH %s. Now: %s, name: %s, coverage: %s, inflation: %s",
				tx.IDShortString(), ledger.TimeNow().String(), sd.Name(),
				util.Th(stemLock.TotalCoverage), util.Th(tx.InflationAmount()))
			return true
		}
		if seq.wontSubmitBranchID != tx.ID() {
			// prevent excess logging of the same message
			sd2 := tx.SequencerTransactionData().SequencerOutputData.SequencerData
			seq.Log().Warnf("WON'T SUBMIT BRANCH %s. reason: insufficient coverage delta. Now: %s, name: %s, cov.delta: %s/%s, supply: %s, infl: %s, slot infl: %s",
				tx.IDShortString(), ledger.TimeNow().String(), sd2.Name(),
				util.Th(stemLock.TotalCoverage), util.Th(stemLock.CoverageDelta), util.Th(stemLock.TotalSupply), util.Th(tx.InflationAmount()), util.Th(stemLock.SlotInflation))
			seq.wontSubmitBranchID = tx.ID()
		}
		return false
	}

	sd3 := tx.SequencerTransactionData().SequencerOutputData.SequencerData
	seq.Log().Infof("SUBMIT SEQ TX %s. Now: %s, name: %s, endorse: %d, coverage: %s, inflation: %s",
		tx.IDShortString(), ledger.TimeNow().String(), sd3.Name(), tx.NumEndorsements(),
		util.Th(ledgerCoverage), util.Th(tx.InflationAmount()))
	return true
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

// OnMilestoneSubmittedVID is a type-agnostic convenience wrapper around OnMilestoneSubmitted.
func (seq *Sequencer) OnMilestoneSubmittedVID(fun func(ms *vertex.WrappedTx)) {
	seq.OnMilestoneSubmitted(func(_ *Sequencer, ms *vertex.WrappedTx) {
		fun(ms)
	})
}

func (seq *Sequencer) OnExitOnce(fun func()) {
	seq.onCallbackMutex.Lock()
	defer seq.onCallbackMutex.Unlock()

	if seq.onExit == nil {
		seq.onExit = fun
	} else {
		prevFun := seq.onExit
		seq.onExit = func() {
			prevFun()
			fun()

			seq.onCallbackMutex.Lock()
			defer seq.onCallbackMutex.Unlock()
			seq.onExit = prevFun
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

func (seq *Sequencer) BacklogTTLSlots() (int, int) {
	return seq.config.BacklogTagAlongTTLSlots, seq.config.BacklogDelegationTTLSlots
}

// bootstrapOwnMilestoneOutput finds this sequencer's chain-output starting point when
// the tippool has no non-virtual own milestone available.
//
// Always starts from the LRB, never from tippool-reported latest milestones. This bounds
// the subsequent attacher past-cone walk to the LRB's committed state — a single trie
// read — rather than letting AttachOutputWithID chase a long chain of uncommitted
// sequencer txs via peer pulls. In a healthy network the LRB is 1–2 slots behind, so
// the regression is negligible; in a degraded network (e.g. consensus halt with many
// uncommitted slots stacked on stale peers), starting from the LRB is the only way the
// sequencer can make progress without blocking for minutes in the boot proposer.
//
// Prior behaviour iterated LatestMilestonesDescending first and could trigger pending-
// branch commits plus deep peer pulls; reverted 2026-04-23 after observing the
// cold-start sequencer deadlock during the halt investigation.
func (seq *Sequencer) bootstrapOwnMilestoneOutput() vertex.WrappedOutput {
	branchData := seq.Branches().FindLatestReliableBranch()
	if branchData == nil {
		seq.Log().Warnf("bootstrapOwnMilestoneOutput: can't find LRB")
		return vertex.WrappedOutput{}
	}
	rdr := multistate.MakeSugared(seq.Branches().GetStateReaderForTheBranch(branchData.TxID()))
	chainOut, err := rdr.GetChainOutputWithID(seq.SequencerID())
	if err != nil {
		seq.Log().Warnf("bootstrapOwnMilestoneOutput: can't load own milestone output from LRB")
		return vertex.WrappedOutput{}
	}
	return attacher.AttachOutputWithID(*chainOut, seq, attacher.WithInvokedBy("bootstrap-from-LRB"))
}

// validateSequencerIDExists checks if the sequencer ID exists in the latest reliable branch.
// Returns false with error log if the chain doesn't exist (likely misconfigured sequencer_id in config).
func (seq *Sequencer) validateSequencerIDExists() bool {
	branchData := seq.Branches().FindLatestReliableBranch()
	if branchData == nil {
		seq.log.Errorf("validateSequencerIDExists: can't find latest reliable branch")
		return false
	}
	rdr := multistate.MakeSugared(seq.Branches().GetStateReaderForTheBranch(branchData.TxID()))
	_, err := rdr.GetChainOutputWithID(seq.sequencerID)
	if err != nil {
		seq.log.Errorf("validateSequencerIDExists: sequencer chain %s not found in ledger state. "+
			"Check 'sequencer_id' in proxima.yaml configuration", seq.sequencerID.String())
		return false
	}
	seq.log.Infof("validateSequencerIDExists: sequencer chain %s found in ledger state", seq.sequencerID.StringShort())
	return true
}

func (seq *Sequencer) generateMilestoneForTarget(targetTs base.LedgerTime) (*transaction.Transaction, *txmetadata.TransactionMetadata, uint64, string, error) {
	// The target timestamp is a logical clock. The wall clock equivalent is informational —
	// the real constraints are sequencer pace and slot boundaries (ledger time).
	// task.Run uses a fixed BuildBudget (wall-clock), so the target can be in the past
	// by up to BuildBudget and still be buildable.
	targetWallClock := ledger.ClockTime(targetTs)
	nowis := time.Now()
	seq.Tracef(TraceTag, "generateMilestoneForTarget: target: %s, wallclock: %s, nowis: %s",
		targetTs.String, targetWallClock.Format("15:04:05.999"), nowis.Format("15:04:05.999"))

	// Reject only if the target is so far in the past that the build budget wouldn't help.
	if behind := targetWallClock.Sub(nowis); behind < -task.BuildBudget {
		return nil, nil, 0, "", fmt.Errorf("sequencer: target %s (%v) is before current clock by %v: too late to generate milestone",
			targetTs.String(), targetWallClock.Format("15:04:05.999"), behind)
	}
	return task.Run(seq, targetTs, seq.slotData)
}

func (seq *Sequencer) NumOutputsInBuffer() int {
	return seq.Backlog().NumOutputsInBuffer()
}

func (seq *Sequencer) NumMilestones() int {
	return seq.NumSequencerTips()
}

// IsVertexReferenced returns true if the vertex is referenced by own milestones or backlog.
func (seq *Sequencer) IsVertexReferenced(vid *vertex.WrappedTx) bool {
	// check own milestones
	seq.ownMilestonesMutex.RLock()
	_, inOwnMilestones := seq.ownMilestones[vid]
	seq.ownMilestonesMutex.RUnlock()
	if inOwnMilestones {
		return true
	}
	// check backlog
	return seq.backlog.IsVertexReferenced(vid)
}
