package workflow

import (
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap"
)

const (
	tracePullEnabled = false
)

func tracePull(log *zap.SugaredLogger, format string, lazyArgs ...any) {
	if tracePullEnabled {
		log.Infof(">>>>>>>>>>>>>>>> TRACE PULL "+format, util.EvalLazyArgs(lazyArgs...)...)
	}
}

func (c *Consumer[T]) tracePull(format string, lazyArgs ...any) {
	if tracePullEnabled {
		tracePull(c.Log(), format, lazyArgs...)
	}
}
