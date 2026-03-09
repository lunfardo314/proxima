package txinput_queue

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/prometheus/client_golang/prometheus"
)

// transaction input queue to buffer incoming transactions from peers and from API
// Maintains bloom filter and check repeating transactions (with small probability of false positives)

type (
	environment interface {
		global.NodeGlobal
		SelfPeerID() peer.ID
		CheckTxSender(tx *transaction.Transaction, meta *txmetadata.TransactionMetadata, fromPeer peer.ID, wanted bool)
	}

	Input struct {
		Cmd byte
		// TxID a prefix of transaction bytes received with CmdFromPeer, otherwise uninterpreted.
		// Should not be trusted. used only for gossip optimization and consistency checking
		// Real txid is calculated during base validation
		PrefixTxID base.TransactionID
		TxBytes    []byte
		TxMetaData *txmetadata.TransactionMetadata
		FromPeer   peer.ID
	}

	TxInputQueue struct {
		environment
		*core_modules.CoreModule[Input]
		// bloom filter
		inGate *inGate[base.TransactionID]
		// metrics
		metrics
	}

	metrics struct {
		inputTxCounter        prometheus.Counter
		pulledTxCounter       prometheus.Counter
		filterHitCounter      prometheus.Counter
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
	recreateMapPeriod       = time.Minute
)

func New(env environment) *TxInputQueue {
	ret := &TxInputQueue{
		environment: env,
		inGate:      newInGate[base.TransactionID](inGateBlackListTTLSlots*ledger.L(0).SlotDuration(), cleanIfExceeds),
	}
	ret.CoreModule = core_modules.New[Input](env, Name, ret.consume)
	ret.CoreModule.Start()

	ret.RepeatInBackground(Name+"_inGateCleanup", blackListCleanupPeriod, func() bool {
		ret.inGate.purgeInGate()
		return true
	})

	ret.RepeatInBackground(Name+"_recreateMap", recreateMapPeriod, func() bool {
		ret.inGate.recreateMap()
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
	// check based on the message prefix, without parsing and computing txid
	pass, wanted := q.inGate.checkPass(inp.PrefixTxID)
	if !pass {
		// repeating transaction
		// reject based on txid prefix, without tx base parsing
		q.filterHitCounter.Inc()
		return
	}
	// now preparse it, calculate txid
	tx, err := transaction.Parse(inp.TxBytes)
	if err != nil {
		q.Log().Warnf("TxInputQueue: %v", err)
		return
	}
	// check if message prefix is equal to txid
	if tx.ID() != inp.PrefixTxID {
		q.Log().Warnf("TxInputQueue: tx message prefix (%s) != real txid (%s). Transaction IGNORED", inp.PrefixTxID.String(), tx.IDString())
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
	if inp.FromPeer == q.SelfPeerID() {
		q.LogTx(time.Now(), "received from sequencer", inp.PrefixTxID)
	} else {
		q.LogTx(time.Now(), fmt.Sprintf("received from peer %s", inp.FromPeer), inp.PrefixTxID)
	}
	// new or pulled transaction -> pass to next step
	q.CheckTxSender(tx, metaData, inp.FromPeer, wanted)
}

func (q *TxInputQueue) fromAPI(inp *Input) {
	from := txmetadata.SourceTypeAPI
	if inp.TxMetaData != nil {
		from = inp.TxMetaData.SourceTypeNonPersistent
	}
	tx, err := transaction.Parse(inp.TxBytes)
	if err != nil {
		q.Log().Warnf("TxInputQueue from '%s': %v", from.String(), err)
		return
	}
	txid := tx.ID()
	pass, _ := q.inGate.checkPass(txid)
	if !pass {
		// repeating transaction
		q.filterHitCounter.Inc()
		return
	}
	q.LogTx(time.Now(), "received from API", txid)
	q.CheckTxSender(tx, nil, "", false)
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
	q.filterHitCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txInputQueue_repeating",
		Help: "number of bloom filter hit",
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
		q.filterHitCounter,
		q.nonSequencerTxCounter,
		q.txBytesSizeReceived,
	)
}

// AddPulledTransaction adds transaction short id to the wanted filter.
// It makes the transaction go directly for attachment without checking other filters and without gossiping
func (q *TxInputQueue) AddPulledTransaction(txid base.TransactionID) {
	q.inGate.addPulled(txid)
}

func (q *TxInputQueue) EvidenceNonSequencerTx() {
	q.nonSequencerTxCounter.Inc()
}
