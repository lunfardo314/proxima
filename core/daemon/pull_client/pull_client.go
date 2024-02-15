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
		global.Glb
		global.TxBytesGet
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

func (d *PullClient) Start() {
	d.MarkStartedComponent(Name)
	d.AddOnClosed(func() {
		d.MarkStoppedComponent(Name)
	})
	d.Queue.Start(d, d.Ctx())
}

func (d *PullClient) Consume(inp *Input) {
	d.Tracef(TraceTag, "Consume: (%d) %s", len(inp.TxIDs), inp.TxIDs[0].StringShort())

	toPull := make([]ledger.TransactionID, 0)
	txBytesList := make([][]byte, 0)

	d.mutex.Lock()
	defer d.mutex.Unlock()

	nextPull := time.Now().Add(pullPeriod)
	for _, txid := range inp.TxIDs {
		if _, already := d.pullList[txid]; already {
			continue
		}
		if txBytesWithMetadata := d.GetTxBytesWithMetadata(&txid); len(txBytesWithMetadata) > 0 {
			d.Tracef(TraceTag, "%s fetched from txBytesStore", txid.StringShort)
			txBytesList = append(txBytesList, txBytesWithMetadata)
		} else {
			d.pullList[txid] = nextPull
			d.Tracef(TraceTag, "%s added to the pull list. Pull list size: %d", txid.StringShort, len(d.pullList))
			toPull = append(toPull, txid)
		}
	}
	go d.transactionInMany(txBytesList)
	go d.QueryTransactionsFromRandomPeer(toPull...)
}

func (d *PullClient) transactionInMany(txBytesList [][]byte) {
	for _, txBytesWithMetadata := range txBytesList {
		metadataBytes, txBytes, err := txmetadata.SplitBytesWithMetadata(txBytesWithMetadata)
		if err != nil {
			d.Environment.Log().Errorf("pull: error while parsing tx metadata: '%v'", err)
			continue
		}
		metadata, err := txmetadata.TransactionMetadataFromBytes(metadataBytes)
		if err != nil {
			d.Environment.Log().Errorf("pull: error while parsing tx metadata: '%v'", err)
			continue
		}
		if metadata == nil {
			metadata = &txmetadata.TransactionMetadata{}
		}
		metadata.SourceTypeNonPersistent = txmetadata.SourceTypeTxStore
		if txid, err := d.TxBytesWithMetadataIn(txBytes, metadata); err != nil {
			txidStr := "<nil>"
			if txid != nil {
				txidStr = txid.StringShort()
			}
			d.Environment.Log().Errorf("pull: tx parse error while pull, txid: %s: '%v'", txidStr, err)
		}
	}
}

const pullLoopPeriod = 50 * time.Millisecond

func (d *PullClient) backgroundLoop() {
	defer d.Environment.Log().Infof("background loop stopped")

	buffer := make([]ledger.TransactionID, 0) // minimize heap use
	for {
		select {
		case <-d.stopBackgroundLoopChan:
			return
		case <-time.After(pullLoopPeriod):
		}
		d.pullAllMatured(buffer)
	}
}

func (d *PullClient) pullAllMatured(buf []ledger.TransactionID) {
	buf = util.ClearSlice(buf)
	toRemove := d.toRemoveSetClone()

	d.mutex.Lock()
	defer d.mutex.Unlock()

	toRemove.ForEach(func(removeTxID ledger.TransactionID) bool {
		delete(d.pullList, removeTxID)
		return true
	})

	nowis := time.Now()
	nextDeadline := nowis.Add(pullPeriod)
	for txid, deadline := range d.pullList {
		if nowis.After(deadline) {
			buf = append(buf, txid)
			d.pullList[txid] = nextDeadline
		}
	}
	if len(buf) > 0 {
		d.QueryTransactionsFromRandomPeer(buf...)
	}
}

func (d *PullClient) StopPulling(txid *ledger.TransactionID) {
	d.toRemoveSetMutex.Lock()
	defer d.toRemoveSetMutex.Unlock()

	d.toRemoveSet.Insert(*txid)
	//q.tracePull("StopPulling: %s. pull list size: %d", func() any { return txid.StringShort() }, len(q.pullList))
}

func (d *PullClient) toRemoveSetClone() set.Set[ledger.TransactionID] {
	d.toRemoveSetMutex.Lock()
	defer d.toRemoveSetMutex.Unlock()

	ret := d.toRemoveSet
	d.toRemoveSet = set.New[ledger.TransactionID]()
	return ret
}
