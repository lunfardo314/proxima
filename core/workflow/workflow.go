package workflow

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/events"
	"github.com/lunfardo314/proxima/core/work_process/poker"
	"github.com/lunfardo314/proxima/core/work_process/pruner"
	"github.com/lunfardo314/proxima/core/work_process/pull_tx_server"
	"github.com/lunfardo314/proxima/core/work_process/snapshot"
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
		PullFromNPeers(nPeers int, txid *ledger.TransactionID) int
		GetOwnSequencerID() *ledger.ChainID
	}
	Workflow struct {
		Environment
		*memdag.MemDAG
		cfg   *ConfigParams
		peers *peering.Peers
		// queues and daemons
		pullTxServer *pull_tx_server.PullTxServer
		poker        *poker.Poker
		events       *events.Events
		txInputQueue *txinput_queue.TxInputQueue
		tippool      *tippool.SequencerTips
		pruner       *pruner.Pruner
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
	ret.tippool = tippool.New(ret)
	ret.txInputQueue = txinput_queue.New(ret)
	ret.pruner = pruner.New(ret)
	snapshot.Start(ret)

	ret.peers.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) {
		ret.TxBytesInFromPeerQueued(txBytes, metadata, from)
	})

	ret.peers.OnReceivePullTxRequest(func(from peer.ID, txid ledger.TransactionID) {
		ret.SendTxQueued(from, txid)
	})

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

func (w *Workflow) SendTxQueued(sendTo peer.ID, txid ledger.TransactionID) {
	w.pullTxServer.Push(&pull_tx_server.Input{
		TxID:   txid,
		PeerID: sendTo,
	})
}
