package workflow

import (
	"github.com/lunfardo314/proxima/core"
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
		c.Log().Infof(">>>>>> TRACE TX "+inp.tx.IDShort()+": "+format, util.EvalLazyArgs(args...)...)
	}
}

func (c *Consumer[T]) traceTxID(txid *core.TransactionID, msg string) {
	if global.TraceTxEnabled() {
		c.Log().Infof(">>>>>> TRACE TxID %s: %s", txid.StringShort(), msg)
	}
}
