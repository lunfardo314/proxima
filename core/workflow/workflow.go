package workflow

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/core_modules/events"
	"github.com/lunfardo314/proxima/core/core_modules/poker"
	"github.com/lunfardo314/proxima/core/core_modules/pull_tx_server"
	"github.com/lunfardo314/proxima/core/core_modules/snapshot"
	"github.com/lunfardo314/proxima/core/core_modules/snapshot_restore"
	syncmod "github.com/lunfardo314/proxima/core/core_modules/forward_sync"
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/core/core_modules/txinput_queue"
	"github.com/lunfardo314/proxima/core/core_modules/txsolicit_queue"
	"github.com/lunfardo314/proxima/core/core_modules/txstore_writer"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.Store
		TxBytesStore() global.TxBytesStore
		PullFromPeers(txid base.TransactionID) int
		GetOwnSequencerID() *base.ChainID
		EvidencePastConeSize(sz int)
		EvidenceBranchMutations(numMutations int)
		EvidenceNumberOfTxDependencies(n int)
		SnapshotBranchID() base.TransactionID
		DurationSinceLastMessageFromPeer() time.Duration
		IsConnectedToNetwork() bool
		SelfPeerID() peer.ID
		EvidenceTxValidationStats(took time.Duration, numIn, numOut int)
		LatestReliableState() (multistate.SugaredStateReader, error)
		EvidenceBranchInflationBonus(ib uint64)
		GetLatestReliableBranch() (ret *multistate.BranchData)
		CheckTxSenderConfig() (checkSeq, checkNonSeq bool)
		// IsVertexReferencedBySequencer returns true if the vertex is still referenced by
		// the sequencer's tippool, backlog, or own milestones. Returns false if no sequencer is running.
		IsVertexReferencedBySequencer(vid *vertex.WrappedTx) bool
	}

	Workflow struct {
		environment
		*memdag.MemDAG
		cfg          *ConfigParams
		peers        *peering.Peers
		earliestSlot uint32 // cached, immutable
		// queues and daemons
		pullTxServer   *pull_tx_server.PullTxServer
		poker          *poker.Poker
		events         *events.Events
		txInputQueue   *txinput_queue.TxInputQueue
		txSolicitQueue *txsolicit_queue.TxSolicitQueue
		txStoreWriter  *txstore_writer.TxStoreWriter
		tippool        *tippool.SequencerTips
		branches       *branches.Branches
		syncModule     *syncmod.Sync
		// particular event handlers
		txListener *txListener
		// pipelineGauge mirrors PipelineSize() into Prometheus. Lives on Workflow
		// (not memDAG) because PipelineSize sums state from queues and caches that
		// memDAG can't reach.
		pipelineGauge prometheus.Gauge
		// latestBranchSlotFromPeers is the highest slot of any branch transaction
		// received and validated from peers. It is the forward-sync anchor (the gap
		// is measured against it, not wall clock). Written by txInputQueue, read by
		// the sync module. Monotonic max.
		latestBranchSlotFromPeers atomic.Uint32
		//
		enableTrace    atomic.Bool
		traceTagsMutex sync.RWMutex
		traceTags      set.Set[string]
	}
)

const recreateMapPeriod = time.Minute

func Start(env environment, peers *peering.Peers, opts ...ConfigOption) *Workflow {
	cfg := defaultConfigParams()
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.log(env.Log())

	ret := &Workflow{
		environment:  env,
		cfg:          &cfg,
		peers:        peers,
		traceTags:    set.New[string](),
		earliestSlot: multistate.FetchEarliestSlot(env.StateStore()),
	}
	ret.MemDAG = memdag.New(ret)
	ret.poker = poker.New(ret)
	ret.events = events.New(ret)
	ret.pullTxServer = pull_tx_server.New(ret)
	ret.tippool = tippool.New(ret)
	ret.branches = branches.New(ret)
	ret.txStoreWriter = txstore_writer.New(ret, ret.TxBytesStore())
	ret.txSolicitQueue = txsolicit_queue.New(ret, ret._attach)
	ret.txInputQueue = txinput_queue.New(ret)
	snapshot.Start(ret)
	snapshot_restore.Start(ret)
	ret.syncModule = syncmod.Start(ret)
	ret.startListeningTransactions()

	ret.peers.OnReceiveTxBytes(func(from peer.ID, txBytes []byte, txIDPrefix base.TransactionID) {
		ret.TxBytesInFromPeerQueued(txBytes, nil, from, txIDPrefix)
	})

	ret.peers.OnReceivePullTxRequest(func(from peer.ID, txid base.TransactionID) {
		ret.pullTxServer.Push(&pull_tx_server.Input{
			TxID:   txid,
			PeerID: from,
		})
	})
	// hopefully protects against memory leak
	ret.RepeatInBackground("workflow_recreate_map_loop", recreateMapPeriod, func() bool {
		ret.RecreateVertexMap()
		return true
	})

	// Prometheus pipeline gauge, fed from the same PipelineSize() that
	// /api/v1/node_info and dagviz use, so the numbers always agree.
	ret.pipelineGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_pipeline_size",
		Help: "total transactions in the pipeline: memDAG vertices + txSolicitQueue + txStoreWriter cache + clock wait counter",
	})
	ret.MetricsRegistry().MustRegister(ret.pipelineGauge)
	ret.RepeatInBackground("workflow-stats", 10*time.Second, func() bool {
		ret.pipelineGauge.Set(float64(ret.PipelineSize()))
		return true
	})

	return ret
}

func StartFromConfig(env environment, peers *peering.Peers) *Workflow {
	opts := make([]ConfigOption, 0)
	if viper.GetBool("workflow.do_not_start_pruner") {
		opts = append(opts, OptionDisableMemDAGGC)
	}
	// 'workflow.max_concurrent_attachers' > 0 overrides the auto (CPU-scaled) cap
	if n := viper.GetInt("workflow.max_concurrent_attachers"); n > 0 {
		opts = append(opts, OptionMaxConcurrentAttachers(n))
	}
	// node-global 'suppress_health_enforcement' also read by the sequencer
	if viper.GetBool("suppress_health_enforcement") {
		opts = append(opts, OptionSuppressHealthEnforcement)
	}
	return Start(env, peers, opts...)
}

// SuppressHealthEnforcement reports whether the attacher should accept unhealthy
// branch transactions (node-global 'suppress_health_enforcement' config key).
func (w *Workflow) SuppressHealthEnforcement() bool {
	return w.cfg.suppressHealthEnforcement
}

// AttachFun returns the attach function for use by txinput_queue.
func (w *Workflow) AttachFun() func(tx *transaction.Transaction, opts ...attacher.AttachTxOption) {
	return w._attach
}
