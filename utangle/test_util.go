package utangle

import (
	"context"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle/attacher"
	"github.com/lunfardo314/proxima/utangle/dag"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap"
)

type testingWorkflow struct {
	*dag.DAG
	txBytesStore global.TxBytesStore
	log          *zap.SugaredLogger
}

func newTestingWorkflow(txBytesStore global.TxBytesStore, dag *dag.DAG) *testingWorkflow {
	return &testingWorkflow{
		txBytesStore: txBytesStore,
		DAG:          dag,
		log:          global.NewLogger("test", zap.InfoLevel, nil, ""),
	}
}

func (w *testingWorkflow) Pull(txid core.TransactionID) {
	txBytes := w.txBytesStore.GetTxBytes(&txid)
	if len(txBytes) == 0 {
		return
	}
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	util.AssertNoError(err, "transaction.FromBytes")
	attacher.AttachTransaction(tx, w, context.Background())
}

func (w *testingWorkflow) OnChangeNotify(onChange, notify *vertex.WrappedTx) {
}

func (w *testingWorkflow) Notify(changed *vertex.WrappedTx) {
}

func (w *testingWorkflow) Log() *zap.SugaredLogger {
	return w.log
}
