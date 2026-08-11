package global

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
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
	logFilename    string // configured log file (logger.output); empty when logging to stdout only
	logVerbosity   int
	topicVerbosity map[string]int
	ctx            context.Context
	stopFun        context.CancelFunc
	logStopOnce    *sync.Once
	isShuttingDown atomic.Bool
	isSnapshotting atomic.Bool
	stopOnce       *sync.Once
	gracefulOnce   *sync.Once // first tripped graceful shutdown wins: reason logged + crash log saved once
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

// syncTargets is the set of branches that some attacher stopped at because of the depth cap:
// the branches forward sync must commit. An attacher adds a target when its backward branch
// walk reaches the cap; the target is removed when that branch is committed. The branch is
// deterministic, so attachers on the same lineage add the same target and Add is idempotent —
// normally the set holds a single element (more only if a fork is still alive at the cap slot).
// It is both the sync-mode signal (non-empty == behind) and what directs forward sync (it drives
// the committed frontier up the lowest-slot target's own lineage, so the forward-commit and
// backward-pull waves meet on the same lineage). See claude/sync_semantics.md.
//
// Process-global (like the running-attacher counter in core/attacher): shared across nodes in a
// multi-node test process. Harmless — a node at the tip never reaches the cap, so the set is empty.
var (
	syncTargetsMutex sync.RWMutex
	syncTargets      = make(map[base.TransactionID]struct{})
)

// AddSyncTarget adds a forward-sync target branch. Returns true if it was newly added (the caller
// logs only then). Idempotent: many attachers add the same deterministic target.
func AddSyncTarget(branchID base.TransactionID) bool {
	syncTargetsMutex.Lock()
	defer syncTargetsMutex.Unlock()
	if _, ok := syncTargets[branchID]; ok {
		return false
	}
	syncTargets[branchID] = struct{}{}
	return true
}

// RemoveSyncTarget removes a target (called when the branch is committed). Returns true if it was
// present.
func RemoveSyncTarget(branchID base.TransactionID) bool {
	syncTargetsMutex.Lock()
	defer syncTargetsMutex.Unlock()
	if _, ok := syncTargets[branchID]; !ok {
		return false
	}
	delete(syncTargets, branchID)
	return true
}

// SyncTargetsPending reports whether any sync target is outstanding. Drives the forward-sync
// trigger and sync-mode load shedding (the node is behind iff this is true).
func SyncTargetsPending() bool {
	syncTargetsMutex.RLock()
	defer syncTargetsMutex.RUnlock()
	return len(syncTargets) > 0
}

// LowestSyncTarget returns the lowest-slot pending target (the nearest one forward sync drives
// toward) and true, or zero and false if the set is empty.
func LowestSyncTarget() (base.TransactionID, bool) {
	syncTargetsMutex.RLock()
	defer syncTargetsMutex.RUnlock()
	var lowest base.TransactionID
	found := false
	for branchID := range syncTargets {
		if !found || branchID.Slot() < lowest.Slot() {
			lowest = branchID
			found = true
		}
	}
	return lowest, found
}

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

// LogFilePath joins the configured logger.directory with the given log basename.
// Empty logger.directory means the current working directory. Several nodes on one machine
// share the same directory but have distinct logger.output basenames.
func LogFilePath(basename string) string {
	return filepath.Join(viper.GetString("logger.directory"), basename)
}

// MaintainLogs rotates/purges the previous live log at logFilename (a full path). Purging is
// per-node: it deletes only files in the log's own directory whose basename matches this node's
// own log basename pattern, so nodes sharing a directory never purge each other's logs. Crash
// logs (basename prefixed with util.CrashLogPrefix) are skipped by the purge unconditionally.
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
			err = util.PurgeFilesInDirectory(filepath.Dir(logFilename), filepath.Base(logFilename)+"*", keepLatest)
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
		if logDir := viper.GetString("logger.directory"); logDir != "" {
			util.AssertNoError(os.MkdirAll(logDir, 0755))
		}
		out = LogFilePath(out)
		output = append(output, out)
		erasedPrev, savedPrev = MaintainLogs(out, viper.GetString("logger.previous"), viper.GetInt("logger.keep_latest_logs"))
	}
	ret := _new(lvl, output)
	ret.logFilename = out

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
		gracefulOnce:       &sync.Once{},
		components:         set.New[string](),
		counters:           make(map[string]int),
		txPullRepeatPeriod: PullRepeatPeriodDefault,
		txPullMaxAttempts:  PullMaxAttemptsDefault,
		gcRequestCh:        make(chan struct{}, 1),
	}
	ret.registerMetrics()
	// save a crash log before any fatal exit (assertion failures, .Fatalf), the same as on graceful shutdown
	ret.SugaredLogger = ret.SugaredLogger.WithOptions(zap.WithFatalHook(crashLogFatalHook{g: ret}))
	return ret
}

// crashLogFatalHook saves a crash log, then performs the standard fatal os.Exit(1). It runs after
// zap has written the fatal entry (with stacktrace), so the crash log captures the fatal reason.
type crashLogFatalHook struct {
	g *Global
}

func (h crashLogFatalHook) OnWrite(_ *zapcore.CheckedEntry, _ []zapcore.Field) {
	h.g.saveCrashLog()
	os.Exit(1)
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

// GracefulShutdown initiates orderly node shutdown with a prominently logged reason and saves a
// crash log. Used for shutdowns triggered by an unexpected condition (depth cap, suspected deadlock,
// branch inconsistency, memory stress, ...) where the preceding log history is worth preserving.
// Callable from any context. Idempotent: only the first call across all goroutines logs its
// reason and saves a crash log; later concurrent calls (e.g. a batch of attachers tripping the
// same condition at once) are no-ops, so the log is not flooded with repeating reasons.
func (l *Global) GracefulShutdown(reason string) {
	l.gracefulShutdown(reason, true)
}

// GracefulShutdownNoCrashLog is like GracefulShutdown but does NOT save a crash log. Used for
// operator-initiated, intentional shutdowns (e.g. SIGINT / Ctrl-C) which are not crashes, so
// accumulating crash logs for them is just noise.
func (l *Global) GracefulShutdownNoCrashLog(reason string) {
	l.gracefulShutdown(reason, false)
}

func (l *Global) gracefulShutdown(reason string, saveCrashLog bool) {
	l.gracefulOnce.Do(func() {
		l.Log().Errorf(">>>>>> GRACEFUL SHUTDOWN: %s. Recommend restarting the node", reason)
		if saveCrashLog {
			l.saveCrashLog()
		}
		l.Stop()
	})
}

// saveCrashLog copies the current log file to crash-<unix time>.log so the reason and the
// preceding history survive the next startup log rotation. Crash logs are never auto-cleaned
// (see util.CrashLogPrefix). No-op when logging to stdout only.
func (l *Global) saveCrashLog() {
	if l.logFilename == "" {
		return
	}
	_ = l.SugaredLogger.Sync() // best-effort flush of buffered log lines before copying
	// crash-<node log basename>.<unix>: the basename keeps crash logs of the several nodes sharing
	// the directory distinct; the timestamp preserves successive crashes of the same node.
	dst := filepath.Join(filepath.Dir(l.logFilename),
		fmt.Sprintf("%s-%s.%d", util.CrashLogPrefix, filepath.Base(l.logFilename), time.Now().Unix()))
	if err := util.CopyFile(l.logFilename, dst); err != nil {
		l.Log().Errorf("failed to save crash log %s: %v", dst, err)
		return
	}
	l.Log().Errorf("crash log saved as %s", dst)
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
	// divide in float: integer Duration division truncates to whole milliseconds, which reported
	// every attachment faster than 1ms as 0 and made the metric useless exactly where it matters
	l.attachmentTimeMilliseconds.Set(float64(time.Since(started)) / float64(time.Millisecond))
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

// healthRelief is the configured relaxation of the healthy-branch fraction over a bounded
// range of slots. Branch health is not a ledger rule, it is the convention honest nodes accept
// in order not to fork, so relaxing it is a decision the whole network takes together: every
// node must run the same window and the same fraction, or nodes disagree about which branches
// are reliable. Intended for restarting a network whose frozen coverage expired while it was
// down and which therefore cannot reconstruct a healthy branch at all.
// A relief fraction below 1/2 lets a minority advance consensus on its own, which is the very
// thing the health threshold prevents — see claude/bootstrap_transactions.md.
type healthRelief struct {
	fromSlot uint32
	toSlot   uint32
	fraction Fraction
}

var healthReliefWindow atomic.Pointer[healthRelief]

// SetHealthRelief installs the relief window. Called once at node startup from the
// 'health_relief' config section; absent config leaves health enforcement at the ledger fraction.
func SetHealthRelief(fromSlot, toSlot uint32, fraction Fraction) error {
	if fromSlot > toSlot {
		return fmt.Errorf("health relief: from_slot %d is after to_slot %d", fromSlot, toSlot)
	}
	if fraction.Denominator <= 0 || fraction.Numerator <= 0 || fraction.Numerator >= fraction.Denominator {
		return fmt.Errorf("health relief: fraction %s must be in (0,1)", fraction.String())
	}
	healthReliefWindow.Store(&healthRelief{fromSlot: fromSlot, toSlot: toSlot, fraction: fraction})
	return nil
}

// HealthRelief returns the configured relief window, if any. For logging and diagnostics.
func HealthRelief() (fromSlot, toSlot uint32, fraction Fraction, ok bool) {
	if r := healthReliefWindow.Load(); r != nil {
		return r.fromSlot, r.toSlot, r.fraction, true
	}
	return 0, 0, Fraction{}, false
}

// FractionHealthyBranchAt returns the healthy-branch fraction which applies to a branch in the
// given slot: the relief fraction inside the configured window, the ledger fraction everywhere
// else. Health is always judged per branch slot — a single fraction cannot describe a search
// that spans slots on both sides of the window.
func FractionHealthyBranchAt(slot uint32) Fraction {
	if r := healthReliefWindow.Load(); r != nil && r.fromSlot <= slot && slot <= r.toSlot {
		return r.fraction
	}
	return FractionHealthyBranch()
}

// IsHealthyBranchAt reports whether a branch in the given slot, with these aggregates, is
// healthy under the fraction which applies to that slot.
func IsHealthyBranchAt(slot uint32, coverageDelta, supply uint64) bool {
	return IsHealthyCoverageDelta(coverageDelta, supply, FractionHealthyBranchAt(slot))
}
