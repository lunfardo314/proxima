package txinput_queue

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/work_process"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/prometheus/client_golang/prometheus"
)

// transaction input queue to buffer incoming transactions from peers and from API
// Maintains bloom filter and check repeating transactions (with small probability of false positives)

type (
	environment interface {
		global.NodeGlobal
		TxInFromPeer(tx *transaction.Transaction, metaData *txmetadata.TransactionMetadata, from peer.ID) error
		TxInFromAPI(tx *transaction.Transaction, trace bool) error
		GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) int
	}

	Input struct {
		Cmd        byte
		TxBytes    []byte
		TxMetaData *txmetadata.TransactionMetadata
		FromPeer   peer.ID
		TraceFlag  bool
	}

	TxInputQueue struct {
		environment
		*work_process.WorkProcess[Input]
		bloomFilter    map[ledger.TransactionIDVeryShort4]time.Time
		bloomFilterTTL time.Duration
		// metrics
		inputTxCounter   prometheus.Counter
		pulledTxCounter  prometheus.Counter
		badTxCounter     prometheus.Counter
		filterHitCounter prometheus.Counter
		gossipedCounter  prometheus.Counter
		queueSize        prometheus.Gauge
	}
)

const (
	CmdFromPeer = byte(iota)
	CmdFromAPI
	CmdPurge
)

const (
	Name                  = "txInputQueue"
	bloomFilterTTLInSlots = 12
	purgePeriod           = 5 * time.Second
)

func New(env environment) *TxInputQueue {
	ret := &TxInputQueue{
		environment:    env,
		bloomFilter:    make(map[ledger.TransactionIDVeryShort4]time.Time),
		bloomFilterTTL: bloomFilterTTLInSlots * ledger.L().ID.SlotDuration(),
	}
	ret.WorkProcess = work_process.New[Input](env, Name, ret.consume)
	ret.WorkProcess.Start()

	ret.RepeatInBackground(Name+"_purge", purgePeriod, func() bool {
		ret.Push(Input{Cmd: CmdPurge}, true)
		return true
	})

	ret.registerMetrics()
	return ret
}

func (q *TxInputQueue) consume(inp Input) {
	q.inputTxCounter.Inc()

	switch inp.Cmd {
	case CmdFromPeer:
		q.fromPeer(&inp)
	case CmdFromAPI:
		q.fromAPI(&inp)
	case CmdPurge:
		q.purge()
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
	metaData := inp.TxMetaData
	if metaData == nil {
		metaData = &txmetadata.TransactionMetadata{}
	}

	if metaData.IsResponseToPull {
		q.pulledTxCounter.Inc()
		// do not check bloom filter if transaction was pulled
		if err = q.TxInFromPeer(tx, metaData, inp.FromPeer); err != nil {
			q.badTxCounter.Inc()
			q.Log().Warn("TxInputQueue from peer %s: %v", inp.FromPeer.String(), err)
		}
		// yet put it into the bloom filter
		q.bloomFilter[tx.ID().VeryShortID4()] = time.Now().Add(q.bloomFilterTTL)
		return
	}
	// check bloom filter
	if _, hit := q.bloomFilter[tx.ID().VeryShortID4()]; hit {
		// filter hit, ignore transaction. May be rare false positive!!
		q.filterHitCounter.Inc()
		return
	}

	// not in filter -> definitely new transaction
	q.bloomFilter[tx.ID().VeryShortID4()] = time.Now().Add(q.bloomFilterTTL)

	if err = q.TxInFromPeer(tx, metaData, inp.FromPeer); err != nil {
		q.badTxCounter.Inc()
		q.Log().Warn("TxInputQueue from peer %s: %v", inp.FromPeer.String(), err)
		return
	}
	// gossiping all new pre-validated and not pulled transactions from peers
	q.GossipTxBytesToPeers(inp.TxBytes, inp.TxMetaData, inp.FromPeer)
	q.gossipedCounter.Inc()
}

func (q *TxInputQueue) fromAPI(inp *Input) {
	tx, err := transaction.FromBytes(inp.TxBytes)
	if err != nil {
		q.badTxCounter.Inc()
		q.Log().Warn("TxInputQueue from API: %v", err)
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

func (q *TxInputQueue) purge() {
	nowis := time.Now()
	toDelete := util.KeysFilteredByValues(q.bloomFilter, func(_ ledger.TransactionIDVeryShort4, deadline time.Time) bool {
		return deadline.After(nowis)
	})
	for _, k := range toDelete {
		delete(q.bloomFilter, k)
	}
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

	q.MetricsRegistry().MustRegister(q.inputTxCounter, q.pulledTxCounter, q.badTxCounter, q.filterHitCounter, q.gossipedCounter, q.queueSize)
}
