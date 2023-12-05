package global

import (
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// Hardcoded tracing

var (
	TracePullEnabled = false
	TraceTxEnabled   = false
)

func ReadInTraceFlags(log *zap.SugaredLogger) {
	if TracePullEnabled = viper.GetBool("debug.trace_workflow.pull"); TracePullEnabled {
		log.Infof("TRACE PULL = ON")
	}
	if TraceTxEnabled = viper.GetBool("debug.trace_workflow.tx"); TraceTxEnabled {
		log.Infof("TRACE TX = ON")
	}
}

func TracePull(log *zap.SugaredLogger, format string, lazyArgs ...any) {
	if TracePullEnabled {
		log.Infof(">>>>>>>>>>>>>>>> TRACE PULL "+format, util.EvalLazyArgs(lazyArgs...)...)
	}
}
