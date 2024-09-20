package txinput_queue

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/work_process"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util/bloomfilter"
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
		// bloomFilterIncoming filters repeating transactions (with some probability of false positives)
		bloomFilterIncoming *bloomfilter.Filter[ledger.TransactionIDVeryShort4]
		// bloomFilterWanted is needed to prevent pulled transactions from being gossiped.
		// It also prevents attack vector of defining wanted transaction by the pulling side,
		// not by responding one
		// Has small probability of false positives. In that case transaction will not be gossiped,
		// so it will have to be pulled by nodes
		bloomFilterWanted *bloomfilter.Filter[ledger.TransactionIDVeryShort4]
		// metrics
		inputTxCounter        prometheus.Counter
		pulledTxCounter       prometheus.Counter
		badTxCounter          prometheus.Counter
		filterHitCounter      prometheus.Counter
		gossipedCounter       prometheus.Counter
		queueSize             prometheus.Gauge
		nonSequencerTxCounter prometheus.Counter
	}
)

const (
	CmdFromPeer = byte(iota)
	CmdFromAPI
)

const (
	Name                           = "txInputQueue"
	bloomFilterIncomingTTLInSlots  = 120 // 20 min
	bloomFilterRequestedTTLInSlots = 12  // 2 min
	purgePeriod                    = 10 * time.Second
)

func New(env environment) *TxInputQueue {
	ret := &TxInputQueue{
		environment:         env,
		bloomFilterIncoming: bloomfilter.New[ledger.TransactionIDVeryShort4](env.Ctx(), bloomFilterIncomingTTLInSlots*ledger.L().ID.SlotDuration(), purgePeriod),
		bloomFilterWanted:   bloomfilter.New[ledger.TransactionIDVeryShort4](env.Ctx(), bloomFilterRequestedTTLInSlots*ledger.L().ID.SlotDuration(), purgePeriod),
	}
	ret.WorkProcess = work_process.New[Input](env, Name, ret.consume)
	ret.WorkProcess.Start()

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

	if hit := q.bloomFilterWanted.CheckAndDelete(tx.ID().VeryShortID4()); hit {
		// pulled transaction arrived
		q.bloomFilterIncoming.Add(tx.ID().VeryShortID4())
		metaData.SourceTypeNonPersistent = txmetadata.SourceTypePulled
		q.pulledTxCounter.Inc()

		if err = q.TxInFromPeer(tx, metaData, inp.FromPeer); err != nil {
			q.badTxCounter.Inc()
			q.Log().Warn("TxInputQueue from peer %s: %v", inp.FromPeer.String(), err)
		}
		return
	}

	// check and update bloom filter
	if hit := q.bloomFilterIncoming.CheckAndUpdate(tx.ID().VeryShortID4()); hit {
		// filter hit, ignore transaction. May be rare false positive!!
		q.filterHitCounter.Inc()
		return
	}

	// not in filter -> definitely new transaction

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

	q.MetricsRegistry().MustRegister(q.inputTxCounter, q.pulledTxCounter, q.badTxCounter, q.filterHitCounter, q.gossipedCounter, q.queueSize, q.nonSequencerTxCounter)
}

// AddWantedTransaction adds transaction short id to the wanted filter.
// It makes the transaction go directly for attachment without checking other filters and without gossiping
func (q *TxInputQueue) AddWantedTransaction(txid *ledger.TransactionID) {
	q.bloomFilterWanted.Add(txid.VeryShortID4())
}

func (q *TxInputQueue) EvidenceNonSequencerTx() {
	q.nonSequencerTxCounter.Inc()
}
