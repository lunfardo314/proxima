package workflow

import (
	"context"

	"github.com/lunfardo314/proxima/core/dag"
	"github.com/lunfardo314/proxima/core/events"
	"github.com/lunfardo314/proxima/core/gossip"
	"github.com/lunfardo314/proxima/core/poker"
	"github.com/lunfardo314/proxima/core/pull_client"
	"github.com/lunfardo314/proxima/core/pull_server"
	"github.com/lunfardo314/proxima/core/txinput"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/testutil"
	"go.uber.org/zap"
)

type (
	Workflow struct {
		*dag.DAG
		txBytesStore global.TxBytesStore
		log          *zap.SugaredLogger
		peers        *peering.Peers
		// queues
		txInput    *txinput.Queue
		pullClient *pull_client.Queue
		pullServer *pull_server.Queue
		gossip     *gossip.Queue
		poker      *poker.Queue
		events     *events.Queue
		//
		debugCounters *testutil.SyncCounters
	}
)

var (
	EventNewGoodTx      = eventtype.RegisterNew[*vertex.WrappedTx]("new good seq")
	EventNewValidatedTx = eventtype.RegisterNew[*vertex.WrappedTx]("new validated")
)

func New(stateStore global.StateStore, txBytesStore global.TxBytesStore, peers *peering.Peers, ctx context.Context) *Workflow {
	ret := &Workflow{
		txBytesStore:  txBytesStore,
		log:           global.NewLogger("glb", zap.InfoLevel, nil, ""),
		DAG:           dag.New(stateStore),
		peers:         peers,
		poker:         poker.Start(ctx),
		events:        events.Start(ctx),
		debugCounters: testutil.NewSynCounters(),
	}
	ret.txInput = txinput.Start(ret, ctx)
	ret.pullClient = pull_client.Start(ret, ctx)
	ret.pullServer = pull_server.Start(ret, ctx)
	ret.gossip = gossip.Start(ret, ctx)
	return ret
}

func (w *Workflow) Log() *zap.SugaredLogger {
	return w.log
}
