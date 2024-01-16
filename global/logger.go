package global

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const TimeLayoutDefault = "01-02 15:04:05.000"

//const TimeLayoutDefault = "2006-01-02 15:04:05.000"

type DefaultLogging struct {
	*zap.SugaredLogger
	enabledTrace   atomic.Bool
	traceTagsMutex sync.RWMutex
	traceTags      set.Set[string]
}

func NewLogger(name string, level zapcore.Level, outputs []string, timeLayout string) *zap.SugaredLogger {
	if len(outputs) == 0 {
		outputs = []string{"stdout"}
	}
	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Development:      true,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      outputs,
		ErrorOutputPaths: outputs,
		DisableCaller:    true,
	}

	if timeLayout == "" {
		cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(TimeLayoutDefault)
	} else {
		cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(timeLayout)
	}

	log, err := cfg.Build()
	common.AssertNoError(err)
	log.Core()
	log = log.WithOptions(zap.IncreaseLevel(level), zap.AddStacktrace(zapcore.FatalLevel))

	return log.Sugar().Named(name)
}

func NewDefaultLogging(name string, lvl zapcore.Level, outputs []string) *DefaultLogging {
	return &DefaultLogging{
		SugaredLogger: NewLogger(name, lvl, outputs, ""),
		traceTags:     set.New[string](),
	}
}

func (l *DefaultLogging) Log() *zap.SugaredLogger {
	return l.SugaredLogger
}

func (l *DefaultLogging) EnableTrace(enable bool) {
	l.enabledTrace.Store(enable)
}

func (l *DefaultLogging) EnableTraceTags(tags string) {
	tSplit := strings.Split(tags, ",")
	l.traceTagsMutex.Lock()
	for _, t := range tSplit {
		l.traceTags.Insert(t)
		l.enabledTrace.Store(true)
	}
	l.traceTagsMutex.Unlock()
	for _, tag := range tSplit {
		l.Tracef(tag, "trace tag enabled")
	}
}

func (l *DefaultLogging) DisableTraceTag(tag string) {
	l.traceTagsMutex.Lock()
	defer l.traceTagsMutex.Unlock()

	l.traceTags.Remove(tag)
	if len(l.traceTags) == 0 {
		l.enabledTrace.Store(true)
	}
}

func (l *DefaultLogging) TraceLog(log *zap.SugaredLogger, tag string, format string, args ...any) {
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

func (l *DefaultLogging) Tracef(tag string, format string, args ...any) {
	l.TraceLog(l.Log(), tag, format, args...)
}
