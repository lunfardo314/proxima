package global

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/ledger"
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
	logVerbosity   int
	ctx            context.Context
	stopFun        context.CancelFunc
	logStopOnce    *sync.Once
	isShuttingDown atomic.Bool
	stopOnce       *sync.Once
	mutex          sync.RWMutex
	components     set.Set[string]
	metrics        *prometheus.Registry
	// statically enabled trace tags
	enabledTrace   atomic.Bool
	traceTagsMutex sync.RWMutex
	traceTags      set.Set[string]
	// dynamic transaction tracing with TTL
	txTraceMutex   sync.RWMutex
	txTraceIDs     map[ledger.TransactionID]time.Time
	txTraceEnabled bool
	// is it the first node in the network
	bootstrapMode bool
	// counters
	countersMutex sync.RWMutex
	counters      map[string]int
	// metrics
	generalPurposeCollectors   map[string]prometheus.Gauge
	attachmentTimeMilliseconds prometheus.Gauge
	attachmentsCounter         prometheus.Counter
	// transaction pull parameters
	// repeat pull after. Default 2 sec
	txPullRepeatPeriod time.Duration
	txPullMaxAttempts  int
	txPullFromPeers    int
}

var knownGeneralPurposeGauges = set.New[string]().Insert("att", "wait", "call", "store", "prop", "close")

// PullTimeout maximum time allowed for the virtual txid become transaction (full vertex)
const (
	PullRepeatPeriodDefault = 2 * time.Second
	PullMaxAttemptsDefault  = 10
	PullFromNumPeersDefault = 2
)

const TraceTag = "global"

func fileExists(name string) bool {
	_, err := os.Stat(name)
	return !os.IsNotExist(err)
}

func NewFromConfig() *Global {
	// always assume INFO level
	lvl := zapcore.InfoLevel

	output := []string{"stderr"}
	erasedPrev := false
	savedPrev := ""
	out := viper.GetString("logger.output")
	if out != "" {
		output = append(output, out)
		if fileExists(out) {
			prev := viper.GetString("logger.previous")
			switch {
			case strings.HasPrefix(prev, "erase"):
				err := os.Remove(out)
				util.AssertNoError(err)
				erasedPrev = true
			case strings.HasPrefix(prev, "save"):
				savedPrev = out + fmt.Sprintf(".%d", uint32(time.Now().Unix()))
				err := os.Rename(out, savedPrev)
				util.AssertNoError(err)

				keepLatest := viper.GetInt("logger.keep_latest_logs")
				err = util.PurgeFilesInDirectory(".", out+"*", keepLatest)
				util.AssertNoError(err)
			}
		}
	}
	bootMode := len(os.Args) >= 2 && os.Args[1] == "boot"
	ret := _new(lvl, output, bootMode)

	if bootMode {
		ret.SugaredLogger.Infof("node is starting in BOOT mode")
	}
	if erasedPrev {
		ret.SugaredLogger.Warnf("previous logfile has been erased")
	}
	if savedPrev != "" {
		ret.SugaredLogger.Warnf("previous logfile has been saved as %s", savedPrev)
	}
	ret.logVerbosity = viper.GetInt("logger.verbosity")
	ret.SugaredLogger.Infof("logger verbosity level is %d", ret.logVerbosity)

	if v := viper.GetInt("transaction_pull.repeat_after_sec"); v > 0 {
		ret.txPullRepeatPeriod = time.Duration(v) * time.Second
	}
	if v := viper.GetInt("transaction_pull.max_attempts"); v > 0 {
		ret.txPullMaxAttempts = v
	}
	if v := viper.GetInt("transaction_pull.from_random_peers"); v > 0 {
		ret.txPullFromPeers = v
	}

	ret.SugaredLogger.Infof("transaction pull paraneters:: repeat period: %v, max attempts: %d, num peers: %d",
		ret.txPullRepeatPeriod, ret.txPullMaxAttempts, ret.txPullFromPeers)

	if viper.GetBool("disable_deadlock_catcher") {
		attacher.EnableDeadlockCatching(0)
		ret.SugaredLogger.Infof("deadlock catching in the attacher has been disabled")
	}
	return ret
}

func NewDefault(bootstrap ...bool) *Global {
	if len(bootstrap) > 0 && bootstrap[0] {
		return _new(zapcore.DebugLevel, []string{"stderr"}, true)
	}
	return _new(zapcore.DebugLevel, []string{"stderr"}, false)
}

func _new(logLevel zapcore.Level, outputs []string, bootstrap bool) *Global {
	ctx, cancelFun := context.WithCancel(context.Background())
	ret := &Global{
		ctx:                ctx,
		logVerbosity:       1,
		metrics:            prometheus.NewRegistry(),
		stopFun:            cancelFun,
		SugaredLogger:      NewLogger("", logLevel, outputs, ""),
		traceTags:          set.New[string](),
		stopOnce:           &sync.Once{},
		logStopOnce:        &sync.Once{},
		components:         set.New[string](),
		txTraceIDs:         make(map[ledger.TransactionID]time.Time),
		bootstrapMode:      bootstrap,
		counters:           make(map[string]int),
		txPullRepeatPeriod: PullRepeatPeriodDefault,
		txPullMaxAttempts:  PullMaxAttemptsDefault,
		txPullFromPeers:    PullFromNumPeersDefault,
	}
	ret.registerMetrics()
	return ret
}

func (l *Global) IsBootstrapMode() bool {
	return l.bootstrapMode
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

func (l *Global) IsShuttingDown() bool {
	return l.isShuttingDown.Load()
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
	if !l.enabledTrace.Load() {
		return
	}

	l.traceTagsMutex.RLock()
	defer l.traceTagsMutex.RUnlock()

	for _, t := range strings.Split(tag, ",") {
		if l.traceTags.Contains(t) {
			l.SugaredLogger.Infof("TRACE(%s) %s", t, fmt.Sprintf(format, lazyargs.Eval(args...)...))
			return
		}
	}
}

const (
	defaultTxTracingTTL = time.Minute
)

func (l *Global) StartTracingTx(txid ledger.TransactionID) {
	l.txTraceMutex.Lock()
	defer l.txTraceMutex.Unlock()

	l.txTraceIDs[txid] = time.Now().Add(defaultTxTracingTTL)
	l.SugaredLogger.Infof("TRACE_TX(%s) started tracing", txid.StringShort())
}

func (l *Global) StopTracingTx(txid ledger.TransactionID) {
	l.txTraceMutex.Lock()
	defer l.txTraceMutex.Unlock()

	if _, found := l.txTraceIDs[txid]; found {
		l.SugaredLogger.Infof("TRACE_TX(%s) stopped tracing", txid.StringShort())
	}
	delete(l.txTraceIDs, txid)

}

func (l *Global) TraceTx(txid *ledger.TransactionID, format string, args ...any) {
	l.txTraceMutex.RLock()
	defer l.txTraceMutex.RUnlock()

	if !l.txTraceEnabled {
		return
	}
	if _, found := l.txTraceIDs[*txid]; !found {
		return
	}

	l.SugaredLogger.Infof("TRACE_TX(%s) %s", txid.StringShort(), fmt.Sprintf(format, lazyargs.Eval(args...)...))
}

const txIDPurgeLoopPeriod = time.Second

func (l *Global) TraceTxEnable() {
	l.txTraceMutex.Lock()
	l.txTraceEnabled = true
	l.txTraceMutex.Unlock()

	l.RepeatInBackground("traceID_purge", txIDPurgeLoopPeriod, func() bool {
		l.purgeTraceTxIDs()
		return true
	})
	l.SugaredLogger.Infof("TRACE_TX enabled")
}

func (l *Global) purgeTraceTxIDs() {
	l.txTraceMutex.Lock()
	defer l.txTraceMutex.Unlock()

	nowis := time.Now()
	var toDelete []ledger.TransactionID

	for txid, ttl := range l.txTraceIDs {
		if nowis.Before(ttl) {
			continue
		}
		if len(toDelete) == 0 {
			toDelete = []ledger.TransactionID{txid}
		} else {
			toDelete = append(toDelete, txid)
		}
	}

	for i := range toDelete {
		delete(l.txTraceIDs, toDelete[i])
		l.SugaredLogger.Infof("TRACE_TX(%s) stopped tracing", toDelete[i].StringShort())
	}
}

func (l *Global) RepeatInBackground(name string, period time.Duration, fun func() bool, skipFirst ...bool) {
	l.MarkWorkProcessStarted(name)
	l.Infof0("[%s] STARTED", name)

	go func() {
		defer func() {
			l.MarkWorkProcessStopped(name)
			l.Infof0("[%s] STOPPED", name)
		}()

		if len(skipFirst) == 0 || !skipFirst[0] {
			fun()
		}
		for {
			select {
			case <-l.Ctx().Done():
				return
			case <-time.After(period):
				if !fun() {
					return
				}
			}
		}
	}()
}

func (l *Global) VerbosityLevel() int {
	return l.logVerbosity
}

func (l *Global) InfofAtLevel(level int, template string, args ...any) {
	if level <= l.logVerbosity {
		l.Infof(template, args...)
	}
}

func (l *Global) Infof0(template string, args ...any) {
	l.InfofAtLevel(0, template, args...)
}

func (l *Global) Infof1(template string, args ...any) {
	l.InfofAtLevel(1, template, args...)
}

func (l *Global) Infof2(template string, args ...any) {
	l.InfofAtLevel(2, template, args...)
}

func (l *Global) ClockCatchUpWithLedgerTime(ts ledger.Time) {
	time.Sleep(time.Until(ts.Time()))

	for ledger.TimeNow().BeforeOrEqual(ts) {
		time.Sleep(5 * time.Millisecond)
	}
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
		Help: "attachment time in milliseconds",
	})
	l.attachmentsCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_glb_attachments_counter",
		Help: "total number of attachments",
	})

	l.MetricsRegistry().MustRegister(l.attachmentsCounter, l.attachmentTimeMilliseconds)

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

func (l *Global) AttachmentFinished(started ...time.Time) {
	l.attachmentsCounter.Inc()
	if len(started) > 0 {
		l.attachmentTimeMilliseconds.Set(float64(time.Since(started[0]) / time.Millisecond))
	}
}

func (l *Global) TxPullParameters() (repeatPeriod time.Duration, maxAttempts int, numPeers int) {
	return l.txPullRepeatPeriod, l.txPullMaxAttempts, l.txPullFromPeers
}
