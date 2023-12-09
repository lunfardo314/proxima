package global

import (
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// Hardcoded tracing

var (
	_tracePullEnabled atomic.Bool
	_traceTxEnabled   atomic.Bool
)

func ReadInTraceFlags(log *zap.SugaredLogger) {
	if tracePullEnabled := viper.GetBool("debug.trace_workflow.pull"); tracePullEnabled {
		_tracePullEnabled.Store(tracePullEnabled)
		log.Infof("TRACE PULL = ON")
	}
	if traceTxEnabled := viper.GetBool("debug.trace_workflow.tx"); traceTxEnabled {
		_traceTxEnabled.Store(traceTxEnabled)
		log.Infof("TRACE TX = ON")
	}
}

func TracePull(log *zap.SugaredLogger, format string, lazyArgs ...any) {
	if _tracePullEnabled.Load() {
		log.Infof(">>>>>>>>>>>>>>>> TRACE PULL "+format, util.EvalLazyArgs(lazyArgs...)...)
	}
}

func SetTracePull(val bool) {
	_tracePullEnabled.Store(val)
}

func SetTraceTx(val bool) {
	_traceTxEnabled.Store(val)
}

func TracePullEnabled() bool {
	return _tracePullEnabled.Load()
}

func TraceTxEnabled() bool {
	return _traceTxEnabled.Load()
}
