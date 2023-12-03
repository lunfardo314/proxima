package global

import (
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap"
)

// Hardcoded tracing

const (
	TracePullEnabled = true
	TraceTxEnabled   = true
)

func TracePull(log *zap.SugaredLogger, format string, lazyArgs ...any) {
	if TracePullEnabled {
		log.Infof(">>>>>>>>>>>>>>>> TRACE PULL "+format, util.EvalLazyArgs(lazyArgs...)...)
	}
}
