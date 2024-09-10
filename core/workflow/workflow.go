package workflow

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/events"
	"github.com/lunfardo314/proxima/core/work_process/gossip"
	"github.com/lunfardo314/proxima/core/work_process/poker"
	"github.com/lunfardo314/proxima/core/work_process/pull_tx_server"
	"github.com/lunfardo314/proxima/core/work_process/sync_client"
	"github.com/lunfardo314/proxima/core/work_process/sync_server"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/core/work_process/txinput_queue"
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
		SyncServerDisabled() bool
		PullFromPeers(txid *ledger.TransactionID)
	}
	Workflow struct {
		Environment
		*memdag.MemDAG
		cfg   *ConfigParams
		peers *peering.Peers
		// daemons
		pullTxServer *pull_tx_server.PullTxServer
		syncServer   *sync_server.SyncServer
		gossip       *gossip.Gossip
		poker        *poker.Poker
		events       *events.Events
		txInputQueue *txinput_queue.TxInputQueue
		tippool      *tippool.SequencerTips
		syncManager  *sync_client.SyncClient
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

func Start(env Environment, peers *peering.Peers, opts ...ConfigOption) *Workflow {
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
	ret.pullTxServer = pull_tx_server.New(ret)
	if !env.SyncServerDisabled() {
		ret.syncServer = sync_server.New(ret)
	}
	ret.gossip = gossip.New(ret)
	ret.tippool = tippool.New(ret)
	ret.txInputQueue = txinput_queue.New(ret)
	if env.SyncServerDisabled() {
		env.Log().Infof("sync server has been disabled")
	}
	if !env.IsBootstrapNode() {
		// bootstrap node does not need sync manager
		ret.syncManager = sync_client.StartSyncClientFromConfig(ret) // nil if disabled
	}

	ret.peers.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) {
		ret.TxBytesInFromPeerQueued(txBytes, metadata, from)
	})

	ret.peers.OnReceivePullTxRequest(func(from peer.ID, txids []ledger.TransactionID) {
		ret.SendTx(from, txids...)
	})

	if !ret.SyncServerDisabled() {
		ret.peers.OnReceivePullSyncPortion(func(from peer.ID, startingFrom ledger.Slot, maxSlots int) {
			ret.syncServer.Push(&sync_server.Input{
				StartFrom: startingFrom,
				MaxSlots:  maxSlots,
				PeerID:    from,
			})
		})
	}

	return ret
}

func StartFromConfig(env Environment, peers *peering.Peers) *Workflow {
	opts := make([]ConfigOption, 0)
	if viper.GetBool("workflow.do_not_start_pruner") {
		opts = append(opts, OptionDoNotStartPruner)
	}
	if viper.GetBool("workflow.sync_manager.enable") {
		opts = append(opts, OptionEnableSyncManager)
	}
	return Start(env, peers, opts...)
}

//
//func (w *Workflow) Start_() {
//	if w.SyncServerDisabled() {
//		w.Log().Infof("sync server has been disabled")
//	}
//	w.poker.Start()
//	w.events.Start()
//	w.pullTxServer.Start()
//	if !w.SyncServerDisabled() {
//		w.syncServer.Start()
//	}
//	w.gossip.Start()
//	w.tippool.Start()
//	w.txInputQueue.Start()
//	if !w.cfg.doNotStartPruner {
//		prune := pruner.New(w) // refactor
//		prune.Start()
//	}
//	if !w.IsBootstrapNode() {
//		// bootstrap node does not need sync manager
//		w.syncManager = sync_client.StartSyncClientFromConfig(w) // nil if disabled
//	}
//	snapshot.Start(w)
//
//	w.peers.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) {
//		w.TxBytesInFromPeerQueued(txBytes, metadata, from)
//	})
//
//	w.peers.OnReceivePullTxRequest(func(from peer.ID, txids []ledger.TransactionID) {
//		w.SendTx(from, txids...)
//	})
//
//	if !w.SyncServerDisabled() {
//		w.peers.OnReceivePullSyncPortion(func(from peer.ID, startingFrom ledger.Slot, maxSlots int) {
//			w.syncServer.Push(&sync_server.Input{
//				StartFrom: startingFrom,
//				MaxSlots:  maxSlots,
//				PeerID:    from,
//			})
//		})
//	}
//}

func (w *Workflow) SendTx(sendTo peer.ID, txids ...ledger.TransactionID) {
	for i := range txids {
		w.pullTxServer.Push(&pull_tx_server.Input{
			TxID:   txids[i],
			PeerID: sendTo,
			PortionInfo: txmetadata.PortionInfo{
				LastIndex: uint16(len(txids) - 1),
				Index:     uint16(i),
			},
		})
	}
}

//
//func (w *Workflow) logSyncStatusLoop() {
//	logSyncStatusEach := ledger.L().ID.SlotDuration() / 2
//	for {
//		select {
//		case <-w.Ctx().Done():
//			return
//		case <-time.After(logSyncStatusEach):
//
//			if !w.IsSynced() {
//				latestSlot, latestHealthySlot, _ := w.LatestBranchSlots()
//				nowSlot := ledger.TimeNow().Slot()
//				w.Log().Warnf("node is NOT SYNCED with the network. Last committed slot is %d (%d slots back). Last healthy slot is %d (%d slots back)",
//					latestSlot, nowSlot-latestSlot, latestHealthySlot, nowSlot-latestHealthySlot)
//			}
//		}
//	}
//}
