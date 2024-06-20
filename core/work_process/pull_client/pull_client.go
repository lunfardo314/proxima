package pull_client

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/queue"
)

const pullPeriod = 500 * time.Millisecond

// pull_client is a queued work process which sends pull requests for a specified transaction
// to a random peer. It repeats pull requests for the transaction periodically until stopped
// TODO in the future tx pulls in the peering network probably will be replaced
//   with direct calls (sync or async) to TxBytesStore server

type (
	Environment interface {
		global.NodeGlobal
		TxBytesStore() global.TxBytesStore
		QueryTransactionsFromRandomPeer(lst ...ledger.TransactionID) bool
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
		pullList map[ledger.TransactionID]time.Time
	}
)

const (
	Name           = "pull_client"
	TraceTag       = Name
	chanBufferSize = 10
)

func New(env Environment) *PullClient {
	return &PullClient{
		Queue:       queue.NewQueueWithBufferSize[*Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment: env,
		pullList:    make(map[ledger.TransactionID]time.Time),
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
		p.pullList[txid] = time.Now().Add(pullPeriod)
		p.Tracef(TraceTag, "%s added to the pull list. Pull list size: %d", txid.StringShort, len(p.pullList))
		p.TraceTx(&txid, TraceTag+": added to the pull list")

		// query from random peer
		go p.QueryTransactionsFromRandomPeer(txid)
	}
}

// transactionIn separates metadata from txBytes and sends it to the workflow input
func (p *PullClient) transactionIn(txBytesWithMetadata []byte) {
	metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
	if err != nil {
		p.Environment.Log().Errorf("pull_client: error while parsing tx metadata: '%v'", err)
		return
	}
	metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
	if err != nil {
		p.Environment.Log().Errorf("pull_client: error while parsing tx metadata: '%v'", err)
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
		p.Environment.Log().Errorf("pull_client: tx parse error while pull, txid: %s: '%v'", txidStr, err)
	}
}

const pullLoopPeriod = 50 * time.Millisecond

// backgroundPullLoop repeats pull requests txids from random peer periodically
func (p *PullClient) backgroundPullLoop() {
	defer p.Environment.Log().Infof("background loop stopped")

	buffer := make([]ledger.TransactionID, 0) // reuse buffer -> minimize heap use

	for {
		select {
		case <-p.Ctx().Done():
			return
		case <-time.After(pullLoopPeriod):
		}

		if buffer = p.maturedPullList(buffer); len(buffer) > 0 {
			p.QueryTransactionsFromRandomPeer(buffer...)
		}
	}
}

// maturedPullList returns list of transaction IDs which should be pulled again.
// reuses the provided buffer and returns new slice
func (p *PullClient) maturedPullList(buf []ledger.TransactionID) []ledger.TransactionID {
	buf = buf[:0]

	p.mutex.Lock()
	defer p.mutex.Unlock()

	nowis := time.Now()
	nextDeadline := nowis.Add(pullPeriod)
	for txid, deadline := range p.pullList {
		if nowis.After(deadline) {
			buf = append(buf, txid)
			p.pullList[txid] = nextDeadline
		}
	}
	return buf
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
