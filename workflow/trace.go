package workflow

import (
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
)

func (c *Consumer[T]) tracePull(format string, lazyArgs ...any) {
	if global.TracePullEnabled() {
		global.TracePull(c.Log(), format, lazyArgs...)
	}
}

func (c *Consumer[T]) traceTx(inp *PrimaryTransactionData, format string, args ...any) {
	if global.TraceTxEnabled() {
		if !inp.traceFlag {
			return
		}
		c.Infof(inp, ">>>>>> TRACE TX "+format, util.EvalLazyArgs(args...)...)
	}
}
