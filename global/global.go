package global

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyargs"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Global struct {
	*zap.SugaredLogger
	outputs        []string
	logVerbosity   int
	topicVerbosity map[string]int
	ctx            context.Context
	stopFun        context.CancelFunc
	logStopOnce    *sync.Once
	isShuttingDown atomic.Bool
	isSnapshotting atomic.Bool
	stopOnce       *sync.Once
	mutex          sync.RWMutex
	components     set.Set[string]
	metrics        *prometheus.Registry
	// statically enabled trace tags
	enabledTrace   atomic.Bool
	traceTagsMutex sync.RWMutex
	traceTags      set.Set[string]
	// counters
	countersMutex sync.RWMutex
	counters      map[string]int
	// metrics
	generalPurposeCollectors   map[string]prometheus.Gauge
	attachmentTimeMilliseconds prometheus.Gauge
	attachmentsCounter         prometheus.Counter
	attachmentCostCounter      prometheus.Counter
	// transaction pull parameters
	// repeat pull after. Default 2 sec
	txPullRepeatPeriod time.Duration
	txPullMaxAttempts  int
	//
	disableDeadlockCatching bool
	// memory pressure management
	memLimitBytes     uint64
	lastPressureGCNs  atomic.Int64    // UnixNano of last actual runtime.GC() from the async worker
	memoryStressLevel atomic.Int32    // current stress level 0-100, updated every stressComputeInterval
	gcRequestCh       chan struct{}   // coalescing request channel for the async GC worker (buffered size 1)
}

var knownGeneralPurposeGauges = set.New[string]().Insert("att", "wait", "call", "store", "prop", "close", "nonseq", "nonseq_drop")

// numAttachersAtMaxDepth counts sequencer attachers currently poll-only at the
// recursion depth cap (waiting for forward sync to deliver a missing branch, not
// pulling). It is the node's sync-mode signal: the node is "behind" iff this is
// non-zero, and forward sync runs exactly while it is non-zero (no "slots behind"
// threshold, no hysteresis). See claude/sync_semantics.md §3-§4.
//
// Process-global (like the running-attacher counter in core/attacher): in a
// multi-node test process it is shared across nodes. Harmless in practice —
// nodes at the tip never reach the cap (per-branch depth ~1), so the counter
// stays 0 unless some node is genuinely many branches behind.
var numAttachersAtMaxDepth atomic.Int32

// IncAttachersAtMaxDepth / DecAttachersAtMaxDepth are called by an attacher when it
// enters / leaves the poll-only-at-cap state. NumAttachersAtMaxDepth is read by
// forward sync to decide whether to run.
func IncAttachersAtMaxDepth()     { numAttachersAtMaxDepth.Add(1) }
func DecAttachersAtMaxDepth()     { numAttachersAtMaxDepth.Add(-1) }
func NumAttachersAtMaxDepth() int { return int(numAttachersAtMaxDepth.Load()) }

// PullTimeout maximum time allowed for the virtual txid become transaction (full vertex)
const (
	PullRepeatPeriodDefault = 2 * time.Second
	PullMaxAttemptsDefault  = 30
)

const TraceTag = "global"

func fileExists(name string) bool {
	_, err := os.Stat(name)
	return !os.IsNotExist(err)
}

func MaintainLogs(logFilename string, prevMode string, keepLatest int) (erasedPrev bool, savedPrev string) {
	if fileExists(logFilename) {
		switch {
		case strings.HasPrefix(prevMode, "erase"):
			err := os.Remove(logFilename)
			util.AssertNoError(err)
			erasedPrev = true
		case strings.HasPrefix(prevMode, "save"):
			savedPrev = logFilename + fmt.Sprintf(".%d", uint32(time.Now().Unix()))
			err := os.Rename(logFilename, savedPrev)
			util.AssertNoError(err)
			err = util.PurgeFilesInDirectory(".", logFilename+"*", keepLatest)
			util.AssertNoError(err)
		}
	}
	return
}

func NewFromConfig() *Global {
	// always assume INFO level
	lvl := zapcore.InfoLevel

	output := []string{"stdout"}
	erasedPrev := false
	savedPrev := ""
	out := viper.GetString("logger.output")
	if out != "" {
		output = append(output, out)
		erasedPrev, savedPrev = MaintainLogs(out, viper.GetString("logger.previous"), viper.GetInt("logger.keep_latest_logs"))
	}
	ret := _new(lvl, output)

	if erasedPrev {
		ret.SugaredLogger.Warnf("previous logfile has been erased")
	}
	if savedPrev != "" {
		ret.SugaredLogger.Warnf("previous logfile has been saved as %s", savedPrev)
	}
	ret.logVerbosity = viper.GetInt("logger.verbosity")
	ret.topicVerbosity = make(map[string]int)
	for k, v := range viper.GetStringMap("logger.topics") {
		switch val := v.(type) {
		case int:
			ret.topicVerbosity[k] = val
		case float64:
			ret.topicVerbosity[k] = int(val)
		case int64:
			ret.topicVerbosity[k] = int(val)
		}
	}
	ret.SugaredLogger.Infof("logger verbosity level is %d", ret.logVerbosity)
	if len(ret.topicVerbosity) > 0 {
		ret.SugaredLogger.Infof("logger topic verbosity: %v", ret.topicVerbosity)
	}

	if v := viper.GetInt("transaction_pull.repeat_after_sec"); v > 0 {
		ret.txPullRepeatPeriod = time.Duration(v) * time.Second
	}
	if v := viper.GetInt("transaction_pull.max_attempts"); v > 0 {
		ret.txPullMaxAttempts = v
	}

	ret.SugaredLogger.Infof("transaction pull parameters:: repeat period: %v, max attempts: %d",
		ret.txPullRepeatPeriod, ret.txPullMaxAttempts)

	ret.disableDeadlockCatching = viper.GetBool("disable_deadlock_catcher")
	if ret.disableDeadlockCatching {
		ret.SugaredLogger.Infof("deadlock catching in the attacher has been disabled")
	}

	if limitMB := viper.GetInt("memory.limit_mb"); limitMB > 0 {
		ret.memLimitBytes = uint64(limitMB) << 20
	}
	ret.startAsyncGCWorker()
	ret.startStressLevelComputation()
	return ret
}

func NewDefault() *Global {
	return _new(zapcore.DebugLevel, nil) // , []string{"stderr"})
}

func _new(logLevel zapcore.Level, outputs []string) *Global {
	ctx, cancelFun := context.WithCancel(context.Background())
	ret := &Global{
		ctx:                ctx,
		outputs:            outputs,
		logVerbosity:       1,
		metrics:            prometheus.NewRegistry(),
		stopFun:            cancelFun,
		SugaredLogger:      NewLogger("", logLevel, outputs, ""),
		traceTags:          set.New[string](),
		stopOnce:           &sync.Once{},
		logStopOnce:        &sync.Once{},
		components:         set.New[string](),
		counters:           make(map[string]int),
		txPullRepeatPeriod: PullRepeatPeriodDefault,
		txPullMaxAttempts:  PullMaxAttemptsDefault,
		gcRequestCh:        make(chan struct{}, 1),
	}
	ret.registerMetrics()
	return ret
}

func (l *Global) MetricsRegistry() *prometheus.Registry {
	return l.metrics
}

func (l *Global) MarkWorkProcessStarted(name string) {
	l.Tracef(TraceTag, "MarkWorkProcessStarted: %s", name)
	l.mutex.Lock()
	defer l.mutex.Unlock()

	util.Assertf(!l.components.Contains(name), "global: repeating work-process %s", name)
	l.components.Insert(name)
}

func (l *Global) MarkWorkProcessStopped(name string) {
	l.Tracef(TraceTag, "MarkWorkProcessStopped: %s", name)
	l.mutex.Lock()
	defer l.mutex.Unlock()

	util.Assertf(l.components.Contains(name), "global: unknown component %s", name)
	l.components.Remove(name)
}

func (l *Global) Stop() {
	l.Tracef(TraceTag, "Stop")
	l.stopOnce.Do(func() {
		l.isShuttingDown.Store(true)
		l.Log().Info("global STOP invoked..")
		l.stopFun()
	})
}

// GracefulShutdown initiates orderly node shutdown with a prominently logged reason.
// Callable from any context. Idempotent — delegates to Stop() which uses sync.Once.
func (l *Global) GracefulShutdown(reason string) {
	l.Log().Errorf(">>>>>> GRACEFUL SHUTDOWN: %s. Recommend restarting the node", reason)
	l.Stop()
}

func (l *Global) IsShuttingDown() bool {
	return l.isShuttingDown.Load()
}

func (l *Global) IsSnapshotting() bool {
	return l.isSnapshotting.Load()
}

func (l *Global) SetSnapshotting(on bool) {
	l.isSnapshotting.Store(on)
}

func (l *Global) MemLimitBytes() uint64 {
	return l.memLimitBytes
}

// ConsensusContribution is the default (no-sequencer) implementation returning 0.
// *ProximaNode overrides it to report its running sequencer's contribution.
func (l *Global) ConsensusContribution() uint64 {
	return 0
}

// MemoryStressLevel returns the current memory stress level (0-100).
// Computed as 100 * allocated / limit. Returns 0 when limit is not configured.
func (l *Global) MemoryStressLevel() int {
	return int(l.memoryStressLevel.Load())
}

const (
	// stressComputeInterval is how often the memory stress level is recomputed.
	stressComputeInterval = 1 * time.Second
)

// startStressLevelComputation starts a background loop that recomputes memory stress every second.
// Also pings the async GC worker when stress crosses stressGCPingPct — this catches bursts
// from operations that don't call MemoryPressureGC directly (e.g. forward-sync batches).
// No-op when memory.limit_mb is not configured.
func (l *Global) startStressLevelComputation() {
	if l.memLimitBytes == 0 {
		return
	}
	l.RepeatInBackground("stress_level", stressComputeInterval, func() bool {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		level := int32(100 * ms.Alloc / l.memLimitBytes)
		if level > 100 {
			level = 100
		}
		l.memoryStressLevel.Store(level)
		if level >= stressGCPingPct {
			l.pingGCWorker()
		}
		return true
	})
}

const (
	memPressureGCPct   = 50                // force GC when heap exceeds this % of limit
	stressGCPingPct    = 60                // stress loop pings the GC worker when level reaches this
	asyncGCMinInterval = 5 * time.Second   // minimum interval between actual runtime.GC() runs in the async worker
)

// MemoryPressureGC is a non-blocking signal that asks the async GC worker to consider running GC.
// Safe to call from any hot path — this function does not run GC itself, only nudges the worker.
// The worker decides whether to actually GC based on heap threshold and rate limit.
// No-op when memory.limit_mb is not configured.
func (l *Global) MemoryPressureGC() {
	if l.memLimitBytes == 0 {
		return
	}
	l.pingGCWorker()
}

// pingGCWorker performs a non-blocking send to the coalescing GC request channel. If a request
// is already pending, this call is a no-op — multiple callers in the same burst collapse into
// a single worker wake-up.
func (l *Global) pingGCWorker() {
	select {
	case l.gcRequestCh <- struct{}{}:
	default:
	}
}

// startAsyncGCWorker launches a single goroutine that serialises runtime.GC() calls off the
// hot paths. The worker blocks on gcRequestCh and, on each request, only runs GC if:
//   - at least asyncGCMinInterval has elapsed since the last GC (rate limit), AND
//   - heap allocation is above memPressureGCPct % of memory.limit_mb.
// Otherwise it no-ops, as per design spec.
// No-op when memory.limit_mb is not configured.
func (l *Global) startAsyncGCWorker() {
	if l.memLimitBytes == 0 {
		return
	}
	const name = "mem_pressure_gc_worker"
	l.MarkWorkProcessStarted(name)
	l.LogTopicf("lifecycle", 0, "[%s] STARTED", name)
	go func() {
		defer func() {
			l.MarkWorkProcessStopped(name)
			l.LogTopicf("lifecycle", 0, "[%s] STOPPED", name)
		}()
		for {
			select {
			case <-l.ctx.Done():
				return
			case <-l.gcRequestCh:
				l.maybeRunGC()
			}
		}
	}()
}

// maybeRunGC is the worker-side decision point: rate limit + heap threshold.
func (l *Global) maybeRunGC() {
	now := time.Now().UnixNano()
	last := l.lastPressureGCNs.Load()
	if now-last < int64(asyncGCMinInterval) {
		return
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	gcThreshold := uint64(float64(l.memLimitBytes) * memPressureGCPct / 100)
	if ms.Alloc <= gcThreshold {
		return
	}
	runtime.GC()
	l.lastPressureGCNs.Store(time.Now().UnixNano())
}

func (l *Global) Ctx() context.Context {
	return l.ctx
}

func (l *Global) _withRLock(fun func()) {
	l.mutex.RLock()
	fun()
	l.mutex.RUnlock()
}

func (l *Global) WaitAllWorkProcessesStop(timeout ...time.Duration) bool {
	l.Tracef(TraceTag, "WaitAllWorkProcessesStop")

	deadline := time.Now().Add(math.MaxInt)
	if len(timeout) > 0 {
		deadline = time.Now().Add(timeout[0])
	}
	exit := false
	for {
		l._withRLock(func() {
			if len(l.components) == 0 {
				l.logStopOnce.Do(func() {
					l.Log().Info("all work processes stopped")
				})
				exit = true
			}
		})
		if exit {
			return true
		}
		time.Sleep(5 * time.Millisecond)
		if time.Now().After(deadline) {
			l._withRLock(func() {
				ln := lines.New()
				for s := range l.components {
					ln.Add(s)
				}
				l.Log().Errorf("WaitAllWorkProcessesStop: exceeded timeout. Still running components: %s", ln.Join(","))
			})
			return false
		}
	}
}

func (l *Global) Outputs() []string {
	return l.outputs
}

func (l *Global) Assertf(cond bool, format string, args ...any) {
	if !l.isShuttingDown.Load() && !cond {
		l.SugaredLogger.Fatalf("assertion failed:: "+format, lazyargs.Eval(args...)...)
	}
}

func (l *Global) AssertNoError(err error, prefix ...string) {
	if err != nil {
		pref := "error: "
		if len(prefix) > 0 {
			pref = strings.Join(prefix, " ") + ": "
		}
		l.SugaredLogger.Fatalf(pref+"%v", err)
	}
}

func (l *Global) AssertMustError(err error) {
	if err == nil {
		l.SugaredLogger.Panicf("AssertMustError: error expected")
	}
}

func (l *Global) Log() *zap.SugaredLogger {
	return l.SugaredLogger
}

func (l *Global) StartTracingTags(tags ...string) {
	func() {
		l.traceTagsMutex.Lock()
		defer l.traceTagsMutex.Unlock()

		for _, t := range tags {
			st := strings.Split(t, ",")
			for _, t1 := range st {
				l.traceTags.Insert(strings.TrimSpace(t1))
			}
			l.enabledTrace.Store(true)
		}
	}()

	for _, tag := range tags {
		l.Tracef(tag, "trace tag enabled")
	}
}

func (l *Global) StopTracingTag(tag string) {
	l.traceTagsMutex.Lock()
	defer l.traceTagsMutex.Unlock()

	l.traceTags.Remove(tag)
	if len(l.traceTags) == 0 {
		l.enabledTrace.Store(false)
	}
}

func (l *Global) Tracef(tag string, format string, args ...any) {
	l.TracefLog(l.SugaredLogger, tag, format, args...)
}

func (l *Global) TracefLog(log *zap.SugaredLogger, tag string, format string, args ...any) {
	if !l.enabledTrace.Load() {
		return
	}

	l.traceTagsMutex.RLock()
	defer l.traceTagsMutex.RUnlock()

	for _, t := range strings.Split(tag, ",") {
		if l.traceTags.Contains(t) {
			log.Infof("TRACE(%s) %s", t, fmt.Sprintf(format, lazyargs.Eval(args...)...))
			return
		}
	}
}

// TopicVerbosityLevel returns the verbosity level for the given topic.
// If the topic is not configured, returns the global verbosity level.
func (l *Global) TopicVerbosityLevel(topic string) int {
	if v, ok := l.topicVerbosity[topic]; ok {
		return v
	}
	return l.logVerbosity
}

// LogTopicf logs a message if the topic's verbosity level is >= requiredLevel.
// Usage: LogTopicf("tag_along", 1, "output %s added", id)
func (l *Global) LogTopicf(topic string, requiredLevel int, template string, args ...any) {
	if requiredLevel <= l.TopicVerbosityLevel(topic) {
		l.Infof(template, args...)
	}
}

// WarnTopicf logs a warning if the topic's verbosity level is >= requiredLevel.
func (l *Global) WarnTopicf(topic string, requiredLevel int, template string, args ...any) {
	if requiredLevel <= l.TopicVerbosityLevel(topic) {
		l.Warnf(template, args...)
	}
}

// ClockCatchUpWithLedgerTime waits until the wall clock catches up with the given ledger time.
// It is context-aware and will return early if the global context is canceled (shutdown).
// Returns true if completed normally (clock caught up), false if interrupted by shutdown.
func (l *Global) ClockCatchUpWithLedgerTime(ts base.LedgerTime) bool {
	targetTime := ledger.ClockTime(ts)
	sleepDuration := time.Until(targetTime)

	if sleepDuration > 0 {
		timer := time.NewTimer(sleepDuration)
		select {
		case <-l.ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}

	// Fine-grained polling loop with context check
	for ledger.TimeNow().BeforeOrEqual(ts) {
		select {
		case <-l.ctx.Done():
			return false
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	return true
}

func (l *Global) IncCounter(name string) {
	l.countersMutex.Lock()
	defer l.countersMutex.Unlock()

	if collector, found := l.generalPurposeCollectors[name]; found {
		collector.Inc()
	}
	l.counters[name] = l.counters[name] + 1
}

func (l *Global) DecCounter(name string) {
	l.countersMutex.Lock()
	defer l.countersMutex.Unlock()

	if collector, found := l.generalPurposeCollectors[name]; found {
		collector.Dec()
	}
	l.counters[name] = l.counters[name] - 1
}

func (l *Global) SetCounter(name string, value int) {
	l.countersMutex.Lock()
	defer l.countersMutex.Unlock()

	if collector, found := l.generalPurposeCollectors[name]; found {
		collector.Set(float64(value))
	}
	l.counters[name] = value
}

func (l *Global) Counter(name string) int {
	l.countersMutex.RLock()
	defer l.countersMutex.RUnlock()

	return l.counters[name]
}

func (l *Global) CounterLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)

	l.countersMutex.RLock()
	defer l.countersMutex.RUnlock()

	for _, k := range util.KeysSorted(l.counters, util.StringsLess) {
		ret.Add("%s: %d", k, l.counters[k])
	}
	return ret
}

func (l *Global) registerMetrics() {
	l.attachmentTimeMilliseconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_glb_attachmentDurationMs",
		Help: "sequencer transaction attachment duration in milliseconds. Does not include branch commitment time, but may include baseline branch commitment time on first reference",
	})
	l.attachmentsCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_glb_attachments_counter",
		Help: "total number of attachments",
	})
	l.attachmentCostCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_glb_attachment_cost_counter",
		Help: "cumulative attachment cost of finished sequencer attachments (past-cone cost + own tx cost)",
	})

	l.MetricsRegistry().MustRegister(l.attachmentsCounter, l.attachmentTimeMilliseconds, l.attachmentCostCounter)

	l.generalPurposeCollectors = make(map[string]prometheus.Gauge)
	knownGeneralPurposeGauges.ForEach(func(name string) bool {
		l.generalPurposeCollectors[name] = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "proxima_general_gauge_" + name,
			Help: fmt.Sprintf("value of the general purpose gauge '%s'", name),
		})
		l.MetricsRegistry().MustRegister(l.generalPurposeCollectors[name])
		return true
	})
}

func (l *Global) AttachmentFinished(started time.Time, cost int) {
	l.attachmentsCounter.Inc()
	l.attachmentTimeMilliseconds.Set(float64(time.Since(started) / time.Millisecond))
	l.attachmentCostCounter.Add(float64(cost))
}

func (l *Global) TxPullParameters() (repeatPeriod time.Duration, maxAttempts int) {
	return l.txPullRepeatPeriod, l.txPullMaxAttempts
}

func (l *Global) DeadlockCatchingDisabled() bool {
	return l.disableDeadlockCatching
}

// LogTx is a no-op implementation of the Logging interface.
// The actual transaction logging is handled at the node level via TxLoggerModule.
func (l *Global) LogTx(_ time.Time, _ string, _ ...base.TransactionID) {
	// no-op: actual logging happens at node level
}

// FractionHealthyBranch returns the healthy-branch coverage fraction
// (numerator/denominator) for the latest ledger library — single source of
// truth, sourced from the ledger constants `constHealthyCoverageNumerator` /
// `constHealthyCoverageDenominator`.
func FractionHealthyBranch() Fraction {
	lib := ledger.L(base.MaxSlot)
	return Fraction{
		Numerator:   int(lib.HealthyCoverageNumerator),
		Denominator: int(lib.HealthyCoverageDenominator),
	}
}
