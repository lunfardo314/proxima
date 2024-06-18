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

type (
	Environment interface {
		global.NodeGlobal
		TxBytesStore() global.TxBytesStore
		QueryTransactionsFromRandomPeer(lst ...ledger.TransactionID) bool
		TxBytesWithMetadataIn(txBytes []byte, metadata *txmetadata.TransactionMetadata) (*ledger.TransactionID, error)
	}

	Input struct {
		TxIDs []ledger.TransactionID
		Stop  bool
	}

	PullClient struct {
		*queue.Queue[*Input]
		Environment
		// set of transaction being pulled
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
	go p.backgroundLoop()
}

func (p *PullClient) Consume(inp *Input) {
	if inp.Stop {
		p.stopPulling(inp.TxIDs)
	} else {
		p.startPulling(inp.TxIDs)
	}
}

func (p *PullClient) startPulling(txids []ledger.TransactionID) {
	toPull := make([]ledger.TransactionID, 0)
	txBytesList := make([][]byte, 0)

	p.mutex.Lock()
	defer p.mutex.Unlock()

	nextPull := time.Now().Add(pullPeriod)
	for _, txid := range txids {
		if _, already := p.pullList[txid]; already {
			continue
		}
		if txBytesWithMetadata := p.TxBytesStore().GetTxBytesWithMetadata(&txid); len(txBytesWithMetadata) > 0 {
			p.Tracef(TraceTag, "%s fetched from txBytesStore", txid.StringShort)
			p.TraceTx(&txid, TraceTag+": fetched from txBytesStore")
			txBytesList = append(txBytesList, txBytesWithMetadata)
		} else {
			p.pullList[txid] = nextPull
			p.Tracef(TraceTag, "%s added to the pull list. Pull list size: %d", txid.StringShort, len(p.pullList))
			p.TraceTx(&txid, TraceTag+": added to the pull list")
			toPull = append(toPull, txid)
		}
	}
	go p.transactionInMany(txBytesList)
	go p.QueryTransactionsFromRandomPeer(toPull...)
}

func (p *PullClient) transactionInMany(txBytesList [][]byte) {
	for _, txBytesWithMetadata := range txBytesList {
		metadataBytes, txBytes, err := txmetadata.SplitTxBytesWithMetadata(txBytesWithMetadata)
		if err != nil {
			p.Environment.Log().Errorf("pull: error while parsing tx metadata: '%v'", err)
			continue
		}
		metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
		if err != nil {
			p.Environment.Log().Errorf("pull: error while parsing tx metadata: '%v'", err)
			continue
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
			p.Environment.Log().Errorf("pull: tx parse error while pull, txid: %s: '%v'", txidStr, err)
		}
	}
}

const pullLoopPeriod = 50 * time.Millisecond

func (p *PullClient) backgroundLoop() {
	defer p.Environment.Log().Infof("background loop stopped")

	buffer := make([]ledger.TransactionID, 0) // minimize heap use
	for {
		select {
		case <-p.Ctx().Done():
			return
		case <-time.After(pullLoopPeriod):
		}
		p.pullAllMatured(buffer)
	}
}

func (p *PullClient) pullAllMatured(buf []ledger.TransactionID) {
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
	if len(buf) > 0 {
		p.QueryTransactionsFromRandomPeer(buf...)
	}
}

func (p *PullClient) Pull(txid *ledger.TransactionID) {
	p.Queue.Push(&Input{
		TxIDs: []ledger.TransactionID{*txid},
	})
}

func (p *PullClient) StopPulling(txid *ledger.TransactionID) {
	p.Queue.Push(&Input{
		TxIDs: []ledger.TransactionID{*txid},
		Stop:  true,
	})
}

func (p *PullClient) stopPulling(txids []ledger.TransactionID) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for _, txid := range txids {
		if _, found := p.pullList[txid]; found {
			delete(p.pullList, txid)
			p.Tracef(TraceTag, "stop pulling %s", txid.StringShort)
			p.TraceTx(&txid, "stop pulling")
		}
	}
}
