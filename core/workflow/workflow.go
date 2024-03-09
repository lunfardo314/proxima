package workflow

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/events"
	"github.com/lunfardo314/proxima/core/work_process/gossip"
	"github.com/lunfardo314/proxima/core/work_process/persist_txbytes"
	"github.com/lunfardo314/proxima/core/work_process/poker"
	"github.com/lunfardo314/proxima/core/work_process/pruner"
	"github.com/lunfardo314/proxima/core/work_process/pull_client"
	"github.com/lunfardo314/proxima/core/work_process/pull_server"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/atomic"
)

type (
	Environment interface {
		global.NodeGlobal
		StateStore() global.StateStore
		TxBytesStore() global.TxBytesStore
	}
	Workflow struct {
		Environment
		*memdag.MemDAG
		peers *peering.Peers
		// daemons
		pullClient       *pull_client.PullClient
		pullServer       *pull_server.PullServer
		gossip           *gossip.Gossip
		persistTxBytes   *persist_txbytes.PersistTxBytes
		poker            *poker.Poker
		events           *events.Events
		tippool          *tippool.SequencerTips
		syncData         *SyncData
		doNotStartPruner bool
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
		MemDAG:           memdag.New(env),
		peers:            peers,
		syncData:         newSyncData(),
		traceTags:        set.New[string](),
		doNotStartPruner: cfg.doNotStartPruner,
	}
	ret.poker = poker.New(ret)
	ret.events = events.New(ret)
	ret.pullClient = pull_client.New(ret)
	ret.pullServer = pull_server.New(ret)
	ret.gossip = gossip.New(ret)
	ret.persistTxBytes = persist_txbytes.New(ret)
	ret.tippool = tippool.New(ret)

	return ret
}

func (w *Workflow) Start() {
	w.Log().Infof("starting work processes...")

	w.poker.Start()
	w.events.Start()
	w.pullClient.Start()
	w.pullServer.Start()
	w.gossip.Start()
	w.persistTxBytes.Start()
	w.tippool.Start()
	if !w.doNotStartPruner {
		prune := pruner.New(w) // refactor
		prune.Start()
	}

	w.peers.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) {
		txid, err := w.TxBytesIn(txBytes, WithPeerMetadata(from, metadata))
		if err != nil {
			txidStr := "(no id)"
			if txid != nil {
				txidStr = txid.StringShort()
			}
			w.Tracef(gossip.TraceTag, "tx-input from peer %s. Parse error: %s -> %v", from.String, txidStr, err)
		} else {
			w.Tracef(gossip.TraceTag, "tx-input from peer %s: %s", from.String, txid.StringShort)
		}
	})

	w.peers.OnReceivePullRequest(func(from peer.ID, txids []ledger.TransactionID) {
		for i := range txids {
			w.Tracef(pull_server.TraceTag, "received pull request for %s", txids[i].StringShort)
			w.pullServer.Push(&pull_server.Input{
				TxID:   txids[i],
				PeerID: from,
			})
		}
	})
}
