package pull_client

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
	"github.com/lunfardo314/proxima/util/set"
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
	}

	PullClient struct {
		*queue.Queue[*Input]
		Environment
		stopBackgroundLoopChan chan struct{}
		// set of transaction being pulled
		mutex    sync.RWMutex
		pullList map[ledger.TransactionID]time.Time
		// list of removed transactions
		toRemoveSetMutex sync.RWMutex
		toRemoveSet      set.Set[ledger.TransactionID]
	}
)

const (
	Name           = "pull_client"
	TraceTag       = Name
	chanBufferSize = 10
)

func New(env Environment) *PullClient {
	return &PullClient{
		Queue:                  queue.NewQueueWithBufferSize[*Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment:            env,
		pullList:               make(map[ledger.TransactionID]time.Time),
		toRemoveSet:            set.New[ledger.TransactionID](),
		stopBackgroundLoopChan: make(chan struct{}),
	}
}

func (p *PullClient) Start() {
	p.MarkWorkProcessStarted(Name)
	p.AddOnClosed(func() {
		p.MarkWorkProcessStopped(Name)
	})
	p.Queue.Start(p, p.Ctx())
}

func (p *PullClient) Consume(inp *Input) {
	p.Tracef(TraceTag, "Consume: (%d) %s", len(inp.TxIDs), inp.TxIDs[0].StringShort())

	toPull := make([]ledger.TransactionID, 0)
	txBytesList := make([][]byte, 0)

	p.mutex.Lock()
	defer p.mutex.Unlock()

	nextPull := time.Now().Add(pullPeriod)
	for _, txid := range inp.TxIDs {
		if _, already := p.pullList[txid]; already {
			continue
		}
		if txBytesWithMetadata := p.TxBytesStore().GetTxBytesWithMetadata(&txid); len(txBytesWithMetadata) > 0 {
			p.Tracef(TraceTag, "%s fetched from txBytesStore", txid.StringShort)
			txBytesList = append(txBytesList, txBytesWithMetadata)
		} else {
			p.pullList[txid] = nextPull
			p.Tracef(TraceTag, "%s added to the pull list. Pull list size: %d", txid.StringShort, len(p.pullList))
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
		case <-p.stopBackgroundLoopChan:
			return
		case <-time.After(pullLoopPeriod):
		}
		p.pullAllMatured(buffer)
	}
}

func (p *PullClient) pullAllMatured(buf []ledger.TransactionID) {
	buf = util.ClearSlice(buf)
	toRemove := p.toRemoveSetClone()

	p.mutex.Lock()
	defer p.mutex.Unlock()

	toRemove.ForEach(func(removeTxID ledger.TransactionID) bool {
		delete(p.pullList, removeTxID)
		return true
	})

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

func (p *PullClient) StopPulling(txid *ledger.TransactionID) {
	p.toRemoveSetMutex.Lock()
	defer p.toRemoveSetMutex.Unlock()

	p.toRemoveSet.Insert(*txid)
	p.Tracef(TraceTag, "stop pulling %s", txid.StringShort)
}

func (p *PullClient) toRemoveSetClone() set.Set[ledger.TransactionID] {
	p.toRemoveSetMutex.Lock()
	defer p.toRemoveSetMutex.Unlock()

	ret := p.toRemoveSet
	p.toRemoveSet = set.New[ledger.TransactionID]()
	return ret
}
