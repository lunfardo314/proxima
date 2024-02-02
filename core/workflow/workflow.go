package workflow

import (
	"context"
	"sync"

	"github.com/lunfardo314/proxima/core/daemon/events"
	"github.com/lunfardo314/proxima/core/daemon/gossip"
	"github.com/lunfardo314/proxima/core/daemon/persist_txbytes"
	"github.com/lunfardo314/proxima/core/daemon/poker"
	"github.com/lunfardo314/proxima/core/daemon/pull_client"
	"github.com/lunfardo314/proxima/core/daemon/pull_server"
	"github.com/lunfardo314/proxima/core/dag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/testutil"
	"go.uber.org/atomic"
)

type (
	Workflow struct {
		*dag.DAG
		*global.DefaultLogging
		global.TxBytesStore
		peers *peering.Peers
		// queues
		pullClient     *pull_client.PullClient
		pullServer     *pull_server.PullServer
		gossip         *gossip.Gossip
		persistTxBytes *persist_txbytes.PersistTxBytes
		poker          *poker.Poker
		events         *events.Events
		syncData       *SyncData
		//
		debugCounters *testutil.SyncCounters
		//
		waitStop sync.WaitGroup
		//
		enableTrace    atomic.Bool
		traceTagsMutex sync.RWMutex
		traceTags      set.Set[string]
	}
)

var (
	EventNewGoodTx = eventtype.RegisterNew[*vertex.WrappedTx]("new good seq")
	EventNewTx     = eventtype.RegisterNew[*vertex.WrappedTx]("new tx") // event may be posted more than once for the transaction
)

func New(stateStore global.StateStore, txBytesStore global.TxBytesStore, peers *peering.Peers, opts ...ConfigOption) *Workflow {
	cfg := defaultConfigParams()
	for _, opt := range opts {
		opt(&cfg)
	}
	lvl := cfg.logLevel

	ret := &Workflow{
		TxBytesStore:   txBytesStore,
		DefaultLogging: global.NewDefaultLogging("", lvl, cfg.logOutput),
		DAG:            dag.New(stateStore),
		peers:          peers,
		syncData:       newSyncData(),
		debugCounters:  testutil.NewSynCounters(),
		traceTags:      set.New[string](),
	}
	ret.poker = poker.New(ret)
	ret.events = events.New(ret)
	ret.pullClient = pull_client.New(ret)
	ret.pullServer = pull_server.New(ret)
	ret.gossip = gossip.New(ret)
	ret.persistTxBytes = persist_txbytes.New(ret)

	return ret
}

func (w *Workflow) Start(ctx context.Context) {
	w.Log().Infof("starting queues...")
	w.waitStop.Add(6)
	w.poker.Start(ctx, &w.waitStop)
	w.events.Start(ctx, &w.waitStop)
	w.pullClient.Start(ctx, &w.waitStop)
	w.pullServer.Start(ctx, &w.waitStop)
	w.gossip.Start(ctx, &w.waitStop)
	w.persistTxBytes.Start(ctx, &w.waitStop)
}

func (w *Workflow) WaitStop() {
	w.Log().Infof("waiting all queues to stop...")
	_ = w.Log().Sync()
	w.waitStop.Wait()
}
