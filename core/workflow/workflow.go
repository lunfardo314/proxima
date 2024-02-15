package workflow

import (
	"sync"

	"github.com/lunfardo314/proxima/core/daemon/events"
	"github.com/lunfardo314/proxima/core/daemon/gossip"
	"github.com/lunfardo314/proxima/core/daemon/persist_txbytes"
	"github.com/lunfardo314/proxima/core/daemon/poker"
	"github.com/lunfardo314/proxima/core/daemon/pruner"
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
	Environment interface {
		global.Glb
		global.StateStore
		global.TxBytesStore
	}
	Workflow struct {
		Environment
		*dag.DAG
		*peering.Peers
		// queues
		pullClient       *pull_client.PullClient
		pullServer       *pull_server.PullServer
		gossip           *gossip.Gossip
		persistTxBytes   *persist_txbytes.PersistTxBytes
		poker            *poker.Poker
		events           *events.Events
		syncData         *SyncData
		doNotStartPruner bool
		//
		debugCounters *testutil.SyncCounters
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

func New(env Environment, peers *peering.Peers, opts ...ConfigOption) *Workflow {
	cfg := defaultConfigParams()
	for _, opt := range opts {
		opt(&cfg)
	}

	ret := &Workflow{
		Environment:      env,
		DAG:              dag.New(env),
		Peers:            peers,
		syncData:         newSyncData(),
		debugCounters:    testutil.NewSynCounters(),
		traceTags:        set.New[string](),
		doNotStartPruner: cfg.doNotStartPruner,
	}
	ret.poker = poker.New(ret)
	ret.events = events.New(ret)
	ret.pullClient = pull_client.New(ret)
	ret.pullServer = pull_server.New(ret)
	ret.gossip = gossip.New(ret)
	ret.persistTxBytes = persist_txbytes.New(ret)

	return ret
}

func (w *Workflow) Start() {
	w.Log().Infof("starting daemons...")

	w.poker.Start()
	w.events.Start()
	w.pullClient.Start()
	w.pullServer.Start()
	w.gossip.Start()
	w.persistTxBytes.Start()
	if !w.doNotStartPruner {
		prune := pruner.New(w.DAG, w) // refactor
		prune.Start()
	}
}
