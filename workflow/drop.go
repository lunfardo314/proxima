package workflow

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util/eventtype"
)

type DropTxData struct {
	TxID       *core.TransactionID
	WhoDropped string
	Msg        string
}

var EventDroppedTx = eventtype.RegisterNew[DropTxData]("droptx")

func (w *Workflow) DropTransaction(txid *core.TransactionID, whoDropped string, reasonFormat string, args ...any) {
	w.PostEvent(EventDroppedTx, DropTxData{
		TxID:       txid,
		WhoDropped: whoDropped,
		Msg:        fmt.Sprintf(reasonFormat, args...),
	})
}
