package workflow

import (
	"context"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/utangle/dag"
	"github.com/lunfardo314/proxima/utangle/events"
	"github.com/lunfardo314/proxima/utangle/poker"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util/eventtype"
	"go.uber.org/zap"
)

type (
	Workflow struct {
		dag          *dag.DAG
		txBytesStore global.TxBytesStore
		log          *zap.SugaredLogger
		poker        *poker.Poker
		events       *events.Events
	}
)

var (
	EventNewGoodTx      = eventtype.RegisterNew[*vertex.WrappedTx]("new good seq")
	EventNewValidatedTx = eventtype.RegisterNew[*vertex.WrappedTx]("new validated")
)

func New(stateStore global.StateStore, txBytesStore global.TxBytesStore, ctx context.Context) *Workflow {
	return &Workflow{
		txBytesStore: txBytesStore,
		dag:          dag.New(stateStore),
		poker:        poker.Start(ctx),
		events:       events.Start(ctx),
		log:          global.NewLogger("glb", zap.InfoLevel, nil, ""),
	}
}

func (w *Workflow) Log() *zap.SugaredLogger {
	return w.log
}
