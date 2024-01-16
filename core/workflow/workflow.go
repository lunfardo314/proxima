package workflow

import (
	"context"
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/core/dag"
	"github.com/lunfardo314/proxima/core/queues/events"
	"github.com/lunfardo314/proxima/core/queues/gossip"
	"github.com/lunfardo314/proxima/core/queues/poker"
	"github.com/lunfardo314/proxima/core/queues/pull_client"
	"github.com/lunfardo314/proxima/core/queues/pull_server"
	"github.com/lunfardo314/proxima/core/queues/txinput"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/set"
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
		txInput    *txinput.TxInput
		pullClient *pull_client.PullClient
		pullServer *pull_server.PullServer
		gossip     *gossip.Gossip
		poker      *poker.Poker
		events     *events.Events
		//
		debugCounters *testutil.SyncCounters
		//
		waitStop sync.WaitGroup
		//
		traceTagsMutex sync.RWMutex
		traceTags      set.Set[string]
	}
)

var (
	EventNewGoodTx      = eventtype.RegisterNew[*vertex.WrappedTx]("new good seq")
	EventNewValidatedTx = eventtype.RegisterNew[*vertex.WrappedTx]("new validated")
)

func New(stateStore global.StateStore, txBytesStore global.TxBytesStore, peers *peering.Peers, opts ...ConfigOption) *Workflow {
	cfg := defaultConfigParams()
	for _, opt := range opts {
		opt(&cfg)
	}
	lvl := cfg.logLevel

	ret := &Workflow{
		txBytesStore:  txBytesStore,
		log:           global.NewLogger("[workflow]", lvl, nil, ""),
		DAG:           dag.New(stateStore),
		peers:         peers,
		poker:         poker.New(lvl),
		events:        events.New(lvl),
		debugCounters: testutil.NewSynCounters(),
		traceTags:     set.New[string](),
	}
	ret.txInput = txinput.New(ret, lvl)
	ret.pullClient = pull_client.New(ret, lvl)
	ret.pullServer = pull_server.New(ret, lvl)
	ret.gossip = gossip.New(ret, lvl)

	return ret
}

func (w *Workflow) Start(ctx context.Context) {
	w.log.Infof("starting queues...")
	w.waitStop.Add(6)
	w.poker.Start(ctx, &w.waitStop)
	w.events.Start(ctx, &w.waitStop)
	w.txInput.Start(ctx, &w.waitStop)
	w.pullClient.Start(ctx, &w.waitStop)
	w.pullServer.Start(ctx, &w.waitStop)
	w.gossip.Start(ctx, &w.waitStop)
}

func (w *Workflow) WaitStop() {
	w.log.Infof("waiting all queues to stop...")
	_ = w.log.Sync()
	w.waitStop.Wait()
}

func (w *Workflow) Log() *zap.SugaredLogger {
	return w.log
}

func (w *Workflow) EnableTraceTag(tag string) {
	w.traceTagsMutex.Lock()
	defer w.traceTagsMutex.Unlock()

	w.traceTags.Insert(tag)
}

func (w *Workflow) DisableTraceTag(tag string) {
	w.traceTagsMutex.Lock()
	defer w.traceTagsMutex.Unlock()

	w.traceTags.Remove(tag)
}

func (w *Workflow) TraceLog(log *zap.SugaredLogger, tag string, format string, args ...any) {
	w.traceTagsMutex.RLock()
	defer w.traceTagsMutex.RUnlock()

	if !w.traceTags.Contains(tag) {
		return
	}

	log.Infof("TRACE [%s] %s", tag, fmt.Sprintf(format, util.EvalLazyArgs(args...)...))
}

func (w *Workflow) Tracef(tag string, format string, args ...any) {
	w.TraceLog(w.log, tag, format, args...)
}
