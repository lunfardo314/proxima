package workflow

import (
	"context"

	"github.com/lunfardo314/proxima/core/dag"
	"github.com/lunfardo314/proxima/core/events"
	"github.com/lunfardo314/proxima/core/poker"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/testutil"
	"go.uber.org/zap"
)

type (
	Workflow struct {
		*dag.DAG
		txBytesStore  global.TxBytesStore
		log           *zap.SugaredLogger
		poker         *poker.Poker
		events        *events.EventQueue
		debugCounters *testutil.SyncCounters
	}
)

var (
	EventNewGoodTx      = eventtype.RegisterNew[*vertex.WrappedTx]("new good seq")
	EventNewValidatedTx = eventtype.RegisterNew[*vertex.WrappedTx]("new validated")
)

func New(stateStore global.StateStore, txBytesStore global.TxBytesStore, ctx context.Context) *Workflow {
	return &Workflow{
		txBytesStore:  txBytesStore,
		DAG:           dag.New(stateStore),
		poker:         poker.Start(ctx),
		events:        events.Start(ctx),
		log:           global.NewLogger("glb", zap.InfoLevel, nil, ""),
		debugCounters: testutil.NewSynCounters(),
	}
}

func (w *Workflow) Log() *zap.SugaredLogger {
	return w.log
}
