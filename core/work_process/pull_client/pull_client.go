package pull_client

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/work_process"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/prometheus/client_golang/prometheus"
)

// pull_client is a queued work process which sends pull requests for a specified transaction
// to a random peer. It repeats pull requests for the transaction periodically until stopped

type (
	environment interface {
		global.NodeGlobal
		TxBytesStore() global.TxBytesStore
		//PullTransactionsFromRandomPeer(lst ...ledger.TransactionID) bool
		PullTransactionsFromAllPeers(lst ...ledger.TransactionID)
		TxBytesWithMetadataIn(txBytes []byte, metadata *txmetadata.TransactionMetadata) (*ledger.TransactionID, error)
	}

	Input struct {
		TxID ledger.TransactionID
		Stop bool
		By   string
	}

	PullClient struct {
		environment
		*work_process.WorkProcess[*Input]
		// set of wanted transactions
		mutex    sync.RWMutex
		pullList map[ledger.TransactionID]pullRecord
		// metrics
		txPullRequestsTotal prometheus.Counter
		txPullRequestsPeers prometheus.Counter
		pullTimeFromPeer    prometheus.Gauge
		numStuckPulls       prometheus.Gauge
	}

	pullRecord struct {
		start        time.Time
		nextDeadline time.Time
	}
)

const (
	Name             = "pullClient"
	TraceTag         = Name
	repeatPullPeriod = 1 * time.Second
	stuckThreshold   = 10 * time.Second
)

func New(env environment) *PullClient {
	ret := &PullClient{
		environment: env,
		pullList:    make(map[ledger.TransactionID]pullRecord),
	}
	ret.WorkProcess = work_process.New[*Input](env, Name, ret.consume)
	ret.registerMetrics()

	ret.RepeatInBackground(Name+"_background_loop", pullLoopPeriod, func() bool {
		txIDs, nStuck := ret.maturedPullList()
		if len(txIDs) > 0 {
			ret.numStuckPulls.Set(float64(nStuck))
			ret.PullTransactionsFromAllPeers(txIDs...)
		}
		return true
	}, true)
	return ret
}

func (p *PullClient) consume(inp *Input) {
	if inp.Stop {
		p.stopPulling(inp.TxID)
	} else {
		p.startPulling(inp.TxID, inp.By)
	}
}

func (p *PullClient) startPulling(txid ledger.TransactionID, by string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if _, already := p.pullList[txid]; already {
		return
	}
	p.txPullRequestsTotal.Inc()

	txBytesWithMetadata := p.TxBytesStore().GetTxBytesWithMetadata(&txid)
	if len(txBytesWithMetadata) > 0 {
		// found transaction bytes in tx store
		p.Tracef(TraceTag, "%s fetched from txBytesStore", txid.StringShort)
		p.TraceTx(&txid, TraceTag+": fetched from txBytesStore")

		// send it into the workflow input
		go p.transactionIn(txBytesWithMetadata)
	} else {
		// transaction is not in the tx store -> query from random peer and put txid into the pull list
		p.pullList[txid] = pullRecord{
			start:        time.Now(),
			nextDeadline: time.Now().Add(repeatPullPeriod),
		}
		p.txPullRequestsPeers.Inc()

		p.Tracef(TraceTag, "%s added to the pull list by %s. Pull list size: %d", txid.StringShort, by, len(p.pullList))
		p.TraceTx(&txid, TraceTag+": added to the pull list")

		// query from 1 random peer
		p.PullTransactionsFromAllPeers(txid)
		//p.PullTransactionsFromRandomPeer(txid)
	}
}

// transactionIn separates metadata from txBytes and sends it to the workflow input
func (p *PullClient) transactionIn(txBytesWithMetadata []byte) {
	metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	if err != nil {
		p.environment.Log().Errorf("[pull_client]: error while parsing tx metadata: '%v'", err)
		return
	}
	metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
	if err != nil {
		p.environment.Log().Errorf("[pull_client]: error while parsing tx metadata: '%v'", err)
		return
	}
	if metadata == nil {
		metadata = &txmetadata.TransactionMetadata{}
	}
	metadata.SourceTypeNonPersistent = txmetadata.SourceTypeTxStore
	if txid, err := p.TxBytesWithMetadataIn(txBytes, metadata); err != nil {
		txidStr := "<nil>"
		if txid != nil {
			txidStr = txid.StringShort()
		}
		p.environment.Log().Errorf("[pull_client]: tx parse error while pull, txid: %s: '%v'", txidStr, err)
	}
}

const pullLoopPeriod = 50 * time.Millisecond

// maturedPullList returns list of transaction IDs which should be pulled again.
// reuses the provided buffer and returns new slice
// Returns also number of transactions which are stuck
func (p *PullClient) maturedPullList() ([]ledger.TransactionID, int) {
	ret := make([]ledger.TransactionID, 0)

	numStuck := 0
	p.mutex.Lock()
	defer p.mutex.Unlock()

	nowis := time.Now()
	nextDeadline := nowis.Add(repeatPullPeriod)
	for txid, rec := range p.pullList {
		if nowis.After(rec.nextDeadline) {
			if time.Since(rec.start) > stuckThreshold {
				numStuck++
			}
			ret = append(ret, txid)
			p.pullList[txid] = pullRecord{
				start:        rec.start,
				nextDeadline: nextDeadline,
			}
		}
	}
	return ret, numStuck
}

// Pull starts pulling txID
func (p *PullClient) Pull(txid ledger.TransactionID, by string) {
	p.Queue.Push(&Input{
		TxID: txid,
		By:   by,
	})
}

// StopPulling stops pulling txID (async)
func (p *PullClient) StopPulling(txid *ledger.TransactionID) {
	p.Queue.Push(&Input{
		TxID: *txid,
		Stop: true,
	})
}

// stopPulling stops pulling txID (sync)
func (p *PullClient) stopPulling(txid ledger.TransactionID) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if rec, found := p.pullList[txid]; found {
		delete(p.pullList, txid)

		p.pullTimeFromPeer.Set(float64(time.Since(rec.start) / time.Millisecond))

		p.Tracef(TraceTag, "stop pulling %s", txid.StringShort)
		p.TraceTx(&txid, "stop pulling")
	}
}

func (p *PullClient) registerMetrics() {
	p.txPullRequestsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txPullRequests_Total_counter",
		Help: "total number of tx pull requests",
	})

	p.txPullRequestsPeers = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_txPullRequestsPeers_counter",
		Help: "number of tx pull requests from peers",
	})

	p.pullTimeFromPeer = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_pullTime_gauge",
		Help: "milliseconds between start and stop pull transaction",
	})

	p.numStuckPulls = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_pullFailed_gauge",
		Help: "number of tx pull requests which are stuck (after timeout)",
	})

	p.MetricsRegistry().MustRegister(p.txPullRequestsTotal, p.txPullRequestsPeers, p.pullTimeFromPeer, p.numStuckPulls)
}
