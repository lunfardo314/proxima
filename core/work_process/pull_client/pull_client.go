package pull_client

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/queue"
)

// pull_client is a queued work process which sends pull requests for a specified transaction
// to a random peer. It repeats pull requests for the transaction periodically until stopped

type (
	Environment interface {
		global.NodeGlobal
		TxBytesStore() global.TxBytesStore
		PullTransactionsFromRandomPeer(lst ...ledger.TransactionID) bool
		PullTransactionsFromAllPeers(lst ...ledger.TransactionID)
		TxBytesWithMetadataIn(txBytes []byte, metadata *txmetadata.TransactionMetadata) (*ledger.TransactionID, error)
	}

	Input struct {
		TxID ledger.TransactionID
		Stop bool
	}

	PullClient struct {
		*queue.Queue[*Input]
		Environment
		// set of wanted transactions
		mutex    sync.RWMutex
		pullList map[ledger.TransactionID]pullRecord
	}

	pullRecord struct {
		start        time.Time
		nextDeadline time.Time
	}
)

const (
	Name                                   = "pullClient"
	TraceTag                               = Name
	chanBufferSize                         = 10
	pullPeriod                             = 500 * time.Millisecond
	startPullingFromAllAfterNumRandomPulls = 10
)

func New(env Environment) *PullClient {
	return &PullClient{
		Queue:       queue.NewQueueWithBufferSize[*Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment: env,
		pullList:    make(map[ledger.TransactionID]pullRecord),
	}
}

func (p *PullClient) Start() {
	p.MarkWorkProcessStarted(Name)
	p.AddOnClosed(func() {
		p.MarkWorkProcessStopped(Name)
	})
	p.Queue.Start(p, p.Ctx())
	go p.backgroundPullLoop()
}

func (p *PullClient) Consume(inp *Input) {
	if inp.Stop {
		p.stopPulling(inp.TxID)
	} else {
		p.startPulling(inp.TxID)
	}
}

func (p *PullClient) startPulling(txid ledger.TransactionID) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if _, already := p.pullList[txid]; already {
		return
	}
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
			nextDeadline: time.Now().Add(pullPeriod),
		}
		p.Tracef(TraceTag, "%s added to the pull list. Pull list size: %d", txid.StringShort, len(p.pullList))
		p.TraceTx(&txid, TraceTag+": added to the pull list")

		// query from 1 random peer
		p.PullTransactionsFromRandomPeer(txid)
	}
}

// transactionIn separates metadata from txBytes and sends it to the workflow input
func (p *PullClient) transactionIn(txBytesWithMetadata []byte) {
	metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	if err != nil {
		p.Environment.Log().Errorf("[pull_client]: error while parsing tx metadata: '%v'", err)
		return
	}
	metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
	if err != nil {
		p.Environment.Log().Errorf("[pull_client]: error while parsing tx metadata: '%v'", err)
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
		p.Environment.Log().Errorf("[pull_client]: tx parse error while pull, txid: %s: '%v'", txidStr, err)
	}
}

const pullLoopPeriod = 50 * time.Millisecond

// backgroundPullLoop repeats pull requests txids from random peer periodically
func (p *PullClient) backgroundPullLoop() {
	defer p.Environment.Log().Infof("[pull_client] background loop stopped")

	buffer := make([]ledger.TransactionID, 0) // reuse buffer -> minimize heap use
	var stuck bool

	for {
		select {
		case <-p.Ctx().Done():
			return
		case <-time.After(pullLoopPeriod):
		}
		if buffer, stuck = p.maturedPullList(buffer); len(buffer) > 0 {
			if stuck {
				// some are stuck, pull from all
				p.PullTransactionsFromAllPeers(buffer...)
			} else {
				// pull from one random
				p.PullTransactionsFromRandomPeer(buffer...)
			}
		}
	}
}

// maturedPullList returns list of transaction IDs which should be pulled again.
// reuses the provided buffer and returns new slice
// Returns true if at least one transaction is stuck for longer than number of periods
func (p *PullClient) maturedPullList(buf []ledger.TransactionID) ([]ledger.TransactionID, bool) {
	buf = buf[:0]

	atLeastOneIsStuck := false
	p.mutex.Lock()
	defer p.mutex.Unlock()

	nowis := time.Now()
	nextDeadline := nowis.Add(pullPeriod)
	for txid, rec := range p.pullList {
		if nowis.After(rec.nextDeadline) {
			if !atLeastOneIsStuck && time.Since(rec.start) > pullPeriod*startPullingFromAllAfterNumRandomPulls {
				atLeastOneIsStuck = true
			}
			buf = append(buf, txid)
			p.pullList[txid] = pullRecord{
				start:        rec.start,
				nextDeadline: nextDeadline,
			}
		}
	}
	return buf, atLeastOneIsStuck
}

// stuckList for debugging
func (p *PullClient) stuckList(forHowLong time.Duration) map[ledger.TransactionID]time.Duration {
	ret := make(map[ledger.TransactionID]time.Duration)

	p.mutex.Lock()
	defer p.mutex.Unlock()

	nowis := time.Now()
	deadline := nowis.Add(-forHowLong)
	for txid, rec := range p.pullList {
		if rec.start.Before(deadline) {
			ret[txid] = time.Since(rec.start)
		}
	}
	return ret
}

func (p *PullClient) printStuckList(forHowLong time.Duration) {
	for txid, howLong := range p.stuckList(forHowLong) {
		p.Environment.Log().Infof(">>>>>>>> [pull_client] %s stuck for %v", txid.StringShort(), howLong)
	}
}

// Pull starts pulling txID
func (p *PullClient) Pull(txid *ledger.TransactionID) {
	p.Queue.Push(&Input{
		TxID: *txid,
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

	if _, found := p.pullList[txid]; found {
		delete(p.pullList, txid)
		p.Tracef(TraceTag, "stop pulling %s", txid.StringShort)
		p.TraceTx(&txid, "stop pulling")
	}
}
