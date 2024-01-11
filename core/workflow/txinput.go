package workflow

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/txinput"
	"github.com/lunfardo314/proxima/ledger"
)

func (w *Workflow) MaxDurationInTheFuture() time.Duration {
	return ledger.TimeSlotDuration()
}

func (w *Workflow) IncCounter(name string) {
	w.debugCounters.Inc(name)
}

func (w *Workflow) StopPulling(txid *ledger.TransactionID) {
	w.log.Infof("StopPulling %s", txid.StringShort())
}

func (w *Workflow) DropTxID(txid *ledger.TransactionID, who string, reasonFormat string, args ...any) {
	w.log.Infof("DropTxID %s", txid.StringShort())
	attacher.InvalidateTxID(*txid, w, fmt.Errorf(reasonFormat, args...))
}

func (w *Workflow) AttachTransaction(inp *txinput.TransactionInputData) {
	attacher.AttachTransaction(inp.Tx, w, attacher.OptionInvokedBy("workflow"))
}

func (w *Workflow) GossipTransaction(inp *txinput.TransactionInputData) {
	w.log.Infof("GossipTransaction %s", inp.Tx.IDShortString())
}
