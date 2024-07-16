package workflow

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/syncmgr"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/events"
	"github.com/lunfardo314/proxima/core/work_process/gossip"
	"github.com/lunfardo314/proxima/core/work_process/persist_txbytes"
	"github.com/lunfardo314/proxima/core/work_process/poker"
	"github.com/lunfardo314/proxima/core/work_process/pruner"
	"github.com/lunfardo314/proxima/core/work_process/pull_client"
	"github.com/lunfardo314/proxima/core/work_process/pull_sync_server"
	"github.com/lunfardo314/proxima/core/work_process/pull_tx_server"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/viper"
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
		cfg   *ConfigParams
		peers *peering.Peers
		// daemons
		pullClient     *pull_client.PullClient
		pullTxServer   *pull_tx_server.PullTxServer
		pullSyncServer *pull_sync_server.PullSyncServer
		gossip         *gossip.Gossip
		persistTxBytes *persist_txbytes.PersistTxBytes
		poker          *poker.Poker
		events         *events.Events
		tippool        *tippool.SequencerTips
		syncManager    *syncmgr.SyncManager
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
	cfg.log(env.Log())

	ret := &Workflow{
		Environment: env,
		MemDAG:      memdag.New(env),
		cfg:         &cfg,
		peers:       peers,
		traceTags:   set.New[string](),
	}
	ret.poker = poker.New(ret)
	ret.events = events.New(ret)
	ret.pullClient = pull_client.New(ret)
	ret.pullTxServer = pull_tx_server.New(ret)
	ret.pullSyncServer = pull_sync_server.New(ret)
	ret.gossip = gossip.New(ret)
	ret.persistTxBytes = persist_txbytes.New(ret)
	ret.tippool = tippool.New(ret)

	return ret
}

func NewFromConfig(env Environment, peers *peering.Peers) *Workflow {
	opts := make([]ConfigOption, 0)
	if viper.GetBool("workflow.do_not_start_pruner") {
		opts = append(opts, OptionDoNotStartPruner)
	}
	if viper.GetBool("workflow.do_not_start_sync_manager") {
		opts = append(opts, OptionDoNotStartSyncManager)
	}
	return New(env, peers, opts...)
}

func (w *Workflow) Start() {
	w.Log().Infof("starting work processes. Ledger time now is %s", ledger.TimeNow().String())

	w.poker.Start()
	w.events.Start()
	w.pullClient.Start()
	w.pullTxServer.Start()
	w.pullSyncServer.Start()
	w.gossip.Start()
	w.persistTxBytes.Start()
	w.tippool.Start()
	if !w.cfg.doNotStartPruner {
		prune := pruner.New(w) // refactor
		prune.Start()
	}
	if !w.cfg.doNotStartSyncManager {
		w.syncManager = syncmgr.StartSyncManager(w)
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
			if txid != nil {
				w.Tracef(gossip.TraceTag, "tx-input from peer %s: %s", from.String, txid.StringShort)
			}
			// txid == nil -> transaction ignored because it is still syncing and lost
		}
	})

	w.peers.OnReceivePullTxRequest(func(from peer.ID, txids []ledger.TransactionID) {
		w.SendTransactions(from, txids)
	})

	w.peers.OnReceivePullSyncPortion(func(from peer.ID, startingFrom ledger.Slot, maxSlots int) {
		w.pullSyncServer.Push(&pull_sync_server.Input{
			StartFrom: startingFrom,
			MaxSlots:  maxSlots,
			PeerID:    from,
		})
	})

	go w.logSyncStatusLoop()
}

func (w *Workflow) SendTransactions(sendTo peer.ID, txids []ledger.TransactionID) {
	for i := range txids {
		w.Tracef(pull_tx_server.TraceTag, "received pull request for %s", txids[i].StringShort)
		w.pullTxServer.Push(&pull_tx_server.Input{
			TxID:   txids[i],
			PeerID: sendTo,
		})
	}
}

const logSyncStatusEach = 2 * time.Second

func (w *Workflow) logSyncStatusLoop() {
	for {
		select {
		case <-w.Ctx().Done():
			return
		case <-time.After(logSyncStatusEach):
			synced, lastSlot := w.SyncedStatus()
			if !synced {
				w.Log().Warnf("node is NOT SYNCED with the network. Last committed slot is %d (%d slots back)",
					lastSlot, ledger.TimeNow().Slot()-lastSlot)
			}
		}
	}
}
