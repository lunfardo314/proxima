package global

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Global struct {
	*zap.SugaredLogger
	*sync.WaitGroup
	enabledTrace   atomic.Bool
	traceTagsMutex sync.RWMutex
	traceTags      set.Set[string]
}

func New(name string, lvl zapcore.Level, outputs []string) *Global {
	return &Global{
		SugaredLogger: NewLogger(name, lvl, outputs, ""),
		traceTags:     set.New[string](),
		WaitGroup:     &sync.WaitGroup{},
	}
}

func (l *Global) MarkStarted() {
	l.WaitGroup.Add(1)
}

func (l *Global) MarkStopped() {
	l.WaitGroup.Done()
}

func (l *Global) Log() *zap.SugaredLogger {
	return l.SugaredLogger
}

func (l *Global) EnableTrace(enable bool) {
	l.enabledTrace.Store(enable)
}

func (l *Global) EnableTraceTags(tags ...string) {
	l.traceTagsMutex.Lock()
	for _, t := range tags {
		st := strings.Split(t, ",")
		for _, t1 := range st {
			l.traceTags.Insert(strings.TrimSpace(t1))
		}
		l.enabledTrace.Store(true)
	}
	l.traceTagsMutex.Unlock()
	for _, tag := range tags {
		l.Tracef(tag, "trace tag enabled")
	}
}

func (l *Global) DisableTraceTag(tag string) {
	l.traceTagsMutex.Lock()
	defer l.traceTagsMutex.Unlock()

	l.traceTags.Remove(tag)
	if len(l.traceTags) == 0 {
		l.enabledTrace.Store(true)
	}
}

func (l *Global) TraceLog(log *zap.SugaredLogger, tag string, format string, args ...any) {
	if !l.enabledTrace.Load() {
		return
	}

	l.traceTagsMutex.RLock()
	defer l.traceTagsMutex.RUnlock()

	for _, t := range strings.Split(tag, ",") {
		if l.traceTags.Contains(t) {
			log.Infof("TRACE(%s) %s", t, fmt.Sprintf(format, util.EvalLazyArgs(args...)...))
			return
		}
	}
}

func (l *Global) Tracef(tag string, format string, args ...any) {
	l.TraceLog(l.Log(), tag, format, args...)
}

type SubLogger struct {
	Logging
}

func MakeSubLogger(l Logging, name string) Logging {
	return SubLogger{&Global{
		SugaredLogger: l.Log().Named(name),
		traceTags:     set.New[string](),
	}}
}
