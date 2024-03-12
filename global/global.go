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

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Global struct {
	*zap.SugaredLogger
	ctx         context.Context
	stopFun     context.CancelFunc
	logStopOnce *sync.Once
	stopOnce    *sync.Once
	mutex       sync.RWMutex
	components  set.Set[string]
	// statically enabled trace tags
	enabledTrace   atomic.Bool
	traceTagsMutex sync.RWMutex
	traceTags      set.Set[string]
	// dynamic transaction tracing with TTL
	enabledTraceTx   atomic.Bool
	txTraceMutex     sync.RWMutex
	txTraceIDs       map[ledger.TransactionID]time.Time
	logAttacherStats bool
}

const TraceTag = "global"

func NewFromConfig() *Global {
	lvlStr := viper.GetString("logger.level")
	lvl := zapcore.InfoLevel
	if lvlStr != "" {
		var err error
		lvl, err = zapcore.ParseLevel(lvlStr)
		util.AssertNoError(err)
	}

	output := []string{"stderr"}
	erasedPrev := false
	out := viper.GetString("logger.output")
	if out != "" {
		output = append(output, out)
		if viper.GetBool("logger.erase_at_start") {
			_ = os.Remove(out)
			erasedPrev = true
		}
	}
	ret := _new(lvl, output)
	if erasedPrev {
		ret.SugaredLogger.Warnf("previous logfile may have been erased")
	}
	ret.logAttacherStats = viper.GetBool("logger.log_attacher_stats")
	return ret
}

func NewDefault() *Global {
	return _new(zapcore.DebugLevel, []string{"stderr"})
}

func _new(logLevel zapcore.Level, outputs []string) *Global {
	ctx, cancelFun := context.WithCancel(context.Background())
	ret := &Global{
		ctx:           ctx,
		stopFun:       cancelFun,
		SugaredLogger: NewLogger("", logLevel, outputs, ""),
		traceTags:     set.New[string](),
		stopOnce:      &sync.Once{},
		logStopOnce:   &sync.Once{},
		components:    set.New[string](),
		txTraceIDs:    make(map[ledger.TransactionID]time.Time),
	}
	go ret.purgeLoop()

	return ret
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
		l.Log().Info("global STOP invoked..")
		l.stopFun()
	})
}

func (l *Global) Ctx() context.Context {
	return l.ctx
}

func (l *Global) _withRLock(fun func()) {
	l.mutex.RLock()
	fun()
	l.mutex.RUnlock()
}

func (l *Global) MustWaitAllWorkProcessesStop(timeout ...time.Duration) {
	l.Tracef(TraceTag, "MustWaitAllWorkProcessesStop")

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
			return
		}
		time.Sleep(5 * time.Millisecond)
		if time.Now().After(deadline) {
			l._withRLock(func() {
				ln := lines.New()
				for s := range l.components {
					ln.Add(s)
				}
				l.Log().Errorf("MustWaitAllWorkProcessesStop: exceeded timeout. Still running components: %s", ln.Join(","))
			})
			return
		}
	}
}

func (l *Global) Assertf(cond bool, format string, args ...any) {
	if !cond {
		l.SugaredLogger.Fatalf("assertion failed:: "+format, util.EvalLazyArgs(args...)...)
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
			l.SugaredLogger.Infof("TRACE(%s) %s", t, fmt.Sprintf(format, util.EvalLazyArgs(args...)...))
			return
		}
	}
}

const (
	defaultTxTracingTTL = time.Minute
	purgeLoopPeriod     = time.Second
)

func (l *Global) StartTracingTx(txid ledger.TransactionID) {
	l.txTraceMutex.Lock()
	defer l.txTraceMutex.Unlock()

	l.txTraceIDs[txid] = time.Now().Add(defaultTxTracingTTL)
	l.enabledTraceTx.Store(true)
	l.SugaredLogger.Infof("TRACE_TX(%s) started tracing", txid.StringShort())
}

func (l *Global) StopTracingTx(txid ledger.TransactionID) {
	l.txTraceMutex.Lock()
	defer l.txTraceMutex.Unlock()

	if _, found := l.txTraceIDs[txid]; found {
		l.SugaredLogger.Infof("TRACE_TX(%s) stopped tracing", txid.StringShort())
	}
	delete(l.txTraceIDs, txid)

	if len(l.txTraceIDs) == 0 {
		l.enabledTraceTx.Store(false)
	}
}

func (l *Global) TraceTx(txid *ledger.TransactionID, format string, args ...any) {
	if !l.enabledTraceTx.Load() {
		return
	}

	l.txTraceMutex.RLock()
	defer l.txTraceMutex.RUnlock()

	if _, found := l.txTraceIDs[*txid]; !found {
		return
	}

	l.SugaredLogger.Infof("TRACE_TX(%s) %s", txid.StringShort(), fmt.Sprintf(format, util.EvalLazyArgs(args...)...))
}

func (l *Global) purgeLoop() {
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-time.After(purgeLoopPeriod):
			l.purge()
		}
	}
}

func (l *Global) LogAttacherStats() bool {
	return l.logAttacherStats
}

func (l *Global) purge() {
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
