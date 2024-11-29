package txinput_queue

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/work_process"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/bytepool"
	"github.com/prometheus/client_golang/prometheus"
)

// transaction input queue to buffer incoming transactions from peers and from API
// Maintains bloom filter and check repeating transactions (with small probability of false positives)

type (
	environment interface {
		global.NodeGlobal
		TxInFromPeer(tx *transaction.Transaction, metaData *txmetadata.TransactionMetadata, from peer.ID) error
		TxInFromAPI(tx *transaction.Transaction, trace bool) error
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID)
	}

	Input struct {
		Cmd        byte
		TxBytes    []byte
		TxMetaData *txmetadata.TransactionMetadata
		FromPeer   peer.ID
		TraceFlag  bool
		TxData     []byte // full data buffer of the transaction
	}

	TxInputQueue struct {
		environment
		*work_process.WorkProcess[Input]
		// bloom filter
		inGate *inGate[ledger.TransactionID]
		// metrics
		metrics
	}

	metrics struct {
		inputTxCounter        prometheus.Counter
		pulledTxCounter       prometheus.Counter
		badTxCounter          prometheus.Counter
		filterHitCounter      prometheus.Counter
		gossipedCounter       prometheus.Counter
		queueSize             prometheus.Gauge
		nonSequencerTxCounter prometheus.Counter
		txBytesSizeReceived   prometheus.Gauge
	}
)

const (
	CmdFromPeer = byte(iota)
	CmdFromAPI
)

const (
	Name = "txInputQueue"

	inGateBlackListTTLSlots = 60 // 10 min
	cleanIfExceeds          = 10_000
	blackListCleanupPeriod  = 10 * time.Second
)

func New(env environment) *TxInputQueue {
	blackTTL := inGateBlackListTTLSlots * ledger.L().ID.SlotDuration()
	ret := &TxInputQueue{
		environment: env,
		inGate:      newInGate[ledger.TransactionID](blackTTL, cleanIfExceeds),
	}
	ret.WorkProcess = work_process.New[Input](env, Name, ret.consume)
	ret.WorkProcess.Start()

	ret.RepeatInBackground(Name+"_inGateCleanup", blackListCleanupPeriod, func() bool {
		ret.inGate.purgeBlackList()
		return true
	})

	ret.registerMetrics()
	return ret
}

func (q *TxInputQueue) consume(inp Input) {
	q.inputTxCounter.Inc()
	q.txBytesSizeReceived.Set(float64(len(inp.TxBytes)))

	switch inp.Cmd {
	case CmdFromPeer:
		q.fromPeer(&inp)
	case CmdFromAPI:
		q.fromAPI(&inp)
	default:
		q.Log().Fatalf("TxInputQueue: wrong cmd")
	}
}

func (q *TxInputQueue) fromPeer(inp *Input) {
	tx, err := transaction.FromBytes(inp.TxBytes)
	if err != nil {
		q.badTxCounter.Inc()
		q.Log().Warn("TxInputQueue: %v", err)
		return
	}
	pass, wanted := q.inGate.checkPass(tx.ID())
	if !pass {
		// repeating transaction
		q.filterHitCounter.Inc()
		// transaction will not be used, return data buffer for reuse
		bytepool.DisposeArray(inp.TxData)
		return
	}

	metaData := inp.TxMetaData
	if metaData == nil {
		metaData = &txmetadata.TransactionMetadata{}
	}
	if wanted {
		// requested transaction
		metaData.SourceTypeNonPersistent = txmetadata.SourceTypePulled
	}
	// new or pulled transaction
	if err = q.TxInFromPeer(tx, metaData, inp.FromPeer); err != nil {
		q.badTxCounter.Inc()
		q.Log().Warn("TxInputQueue from peer %s: %v", inp.FromPeer.String(), err)
		// transaction will not be used, return data buffer for reuse
		bytepool.DisposeArray(inp.TxData)
		return
	}
	if !wanted {
		// gossiping all new pre-validated and not pulled transactions from peers
		q.GossipTxBytesToPeers(inp.TxBytes, inp.TxMetaData, inp.FromPeer)
		q.gossipedCounter.Inc()
	}
}

func (q *TxInputQueue) fromAPI(inp *Input) {
	tx, err := transaction.FromBytes(inp.TxBytes)
	if err != nil {
		q.badTxCounter.Inc()
		q.Log().Warn("TxInputQueue from API: %v", err)
		return
	}
	pass, _ := q.inGate.checkPass(tx.ID())
	if !pass {
		// repeating transaction
		q.filterHitCounter.Inc()
		return
	}
	if err = q.TxInFromAPI(tx, inp.TraceFlag); err != nil {
		q.badTxCounter.Inc()
		q.Log().Warn("TxInputQueue from API: %v", err)
		return
	}
	// gossiping all pre-validated transactions from API
	q.GossipTxBytesToPeers(inp.TxBytes, inp.TxMetaData)
	q.gossipedCounter.Inc()
}

func (q *TxInputQueue) registerMetrics() {
	q.inputTxCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_in",
		Help: "input queue counter",
	})
	q.pulledTxCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_pulled",
		Help: "number of pulled transactions",
	})
	q.badTxCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_bad",
		Help: "number of non-parseable transaction messages",
	})
	q.filterHitCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_repeating",
		Help: "number of bloom filter hit",
	})
	q.gossipedCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_gossiped",
		Help: "number of gossiped",
	})
	q.queueSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_txInputQueue_queueSize",
		Help: "size of the input queue",
	})
	q.nonSequencerTxCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_nonSequencer",
		Help: "number of non-sequencer transactions",
	})
	q.txBytesSizeReceived = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_txInputQueue_txBytesSize",
		Help: "size of the received transaction bytes",
	})

	q.MetricsRegistry().MustRegister(
		q.inputTxCounter,
		q.pulledTxCounter,
		q.badTxCounter,
		q.filterHitCounter,
		q.gossipedCounter,
		q.queueSize,
		q.nonSequencerTxCounter,
		q.txBytesSizeReceived,
	)
}

// AddWantedTransaction adds transaction short id to the wanted filter.
// It makes the transaction go directly for attachment without checking other filters and without gossiping
func (q *TxInputQueue) AddWantedTransaction(txid *ledger.TransactionID) {
	q.inGate.addWanted(*txid)
}

func (q *TxInputQueue) EvidenceNonSequencerTx() {
	q.nonSequencerTxCounter.Inc()
}
