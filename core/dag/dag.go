package dag

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/exp/maps"
)

type (
	DAG struct {
		mutex            sync.RWMutex
		stateStore       global.StateStore
		vertices         map[ledger.TransactionID]*vertex.WrappedTx
		latestTimestamp  ledger.Time
		latestBranchSlot ledger.Slot

		stateReadersMutex sync.Mutex
		stateReaders      map[ledger.TransactionID]*cachedStateReader
	}

	cachedStateReader struct {
		global.IndexedStateReader
		lastActivity time.Time
	}
)

func New(stateStore global.StateStore) *DAG {
	return &DAG{
		stateStore:   stateStore,
		vertices:     make(map[ledger.TransactionID]*vertex.WrappedTx),
		stateReaders: make(map[ledger.TransactionID]*cachedStateReader),
	}
}

const sharedStateReaderCacheSize = 3000

func (d *DAG) StateStore() global.StateStore {
	return d.stateStore
}

func (d *DAG) WithGlobalWriteLock(fun func()) {
	d.mutex.Lock()
	fun()
	d.mutex.Unlock()
}

func (d *DAG) GetVertexNoLock(txid *ledger.TransactionID) *vertex.WrappedTx {
	return d.vertices[*txid]
}

func (d *DAG) GetVertex(txid *ledger.TransactionID) *vertex.WrappedTx {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.GetVertexNoLock(txid)
}

func (d *DAG) NumVertices() int {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return len(d.vertices)
}

func (d *DAG) AddVertexNoLock(vid *vertex.WrappedTx) {
	util.Assertf(d.GetVertexNoLock(&vid.ID) == nil, "d.GetVertexNoLock(vid.ID())==nil")
	d.vertices[vid.ID] = vid
	if d.latestTimestamp.Before(vid.ID.Timestamp()) {
		d.latestTimestamp = vid.ID.Timestamp()
		if vid.IsBranchTransaction() {
			d.latestBranchSlot = vid.ID.Slot()
		}
	}
}

// PurgeDeletedVertices with global lock
func (d *DAG) PurgeDeletedVertices(deleted []*vertex.WrappedTx) {
	d.WithGlobalWriteLock(func() {
		for _, vid := range deleted {
			delete(d.vertices, vid.ID)
		}
	})
}

var stateReaderCacheTTL = 3 * ledger.SlotDuration()

func (d *DAG) PurgeCachedStateReaders() {
	d.stateReadersMutex.Lock()
	defer d.stateReadersMutex.Unlock()

	deadline := time.Now().Add(-stateReaderCacheTTL)
	toDelete := make([]ledger.TransactionID, 0)

	for txid, b := range d.stateReaders {
		if b.lastActivity.Before(deadline) {
			toDelete = append(toDelete, txid)
		}
	}
	for i := range toDelete {
		delete(d.stateReaders, toDelete[i])
	}
}

func (d *DAG) GetStateReaderForTheBranch(branch *ledger.TransactionID) global.IndexedStateReader {
	util.Assertf(branch != nil && branch.IsBranchTransaction(), "branch != nil && branch.IsBranchTransaction()")

	d.stateReadersMutex.Lock()
	defer d.stateReadersMutex.Unlock()

	ret := d.stateReaders[*branch]
	if ret != nil {
		ret.lastActivity = time.Now()
		return ret.IndexedStateReader
	}
	rootRecord, found := multistate.FetchRootRecord(d.stateStore, *branch)
	if !found {
		return nil
	}
	d.stateReaders[*branch] = &cachedStateReader{
		IndexedStateReader: multistate.MustNewReadable(d.stateStore, rootRecord.Root, sharedStateReaderCacheSize),
		lastActivity:       time.Now(),
	}

	return d.stateReaders[*branch]
}

func (d *DAG) GetStemWrappedOutput(branch *ledger.TransactionID) (ret vertex.WrappedOutput) {
	if vid := d.GetVertex(branch); vid != nil {
		ret = vid.StemWrappedOutput()
	}
	return
}

func (d *DAG) GetIndexedStateReader(branchTxID *ledger.TransactionID, clearCacheAtSize ...int) (global.IndexedStateReader, error) {
	rr, found := multistate.FetchRootRecord(d.stateStore, *branchTxID)
	if !found {
		return nil, fmt.Errorf("root record for %s has not been found", branchTxID.StringShort())
	}
	return multistate.NewReadable(d.stateStore, rr.Root, clearCacheAtSize...)
}

func (d *DAG) MustGetIndexedStateReader(branchTxID *ledger.TransactionID, clearCacheAtSize ...int) global.IndexedStateReader {
	ret, err := d.GetIndexedStateReader(branchTxID, clearCacheAtSize...)
	util.AssertNoError(err)
	return ret
}

func (d *DAG) HeaviestStateForLatestTimeSlotWithBaseline() (multistate.SugaredStateReader, *vertex.WrappedTx) {
	branchRecords := multistate.FetchLatestBranches(d.stateStore)
	util.Assertf(len(branchRecords) > 0, "len(branchRecords)>0")

	return multistate.MakeSugared(multistate.MustNewReadable(d.stateStore, branchRecords[0].Root, 0)),
		d.GetVertex(branchRecords[0].TxID())
}

func (d *DAG) HeaviestStateForLatestTimeSlot() multistate.SugaredStateReader {
	rootRecords := multistate.FetchLatestRootRecords(d.stateStore)
	util.Assertf(len(rootRecords) > 0, "len(rootRecords)>0")

	return multistate.MakeSugared(multistate.MustNewReadable(d.stateStore, rootRecords[0].Root, 0))
}

// WaitUntilTransactionInHeaviestState for testing mostly
func (d *DAG) WaitUntilTransactionInHeaviestState(txid ledger.TransactionID, timeout ...time.Duration) (*vertex.WrappedTx, error) {
	deadline := time.Now().Add(10 * time.Minute)
	if len(timeout) > 0 {
		deadline = time.Now().Add(timeout[0])
	}
	for {
		rdr, baseline := d.HeaviestStateForLatestTimeSlotWithBaseline()
		if rdr.KnowsCommittedTransaction(&txid) {
			return baseline, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("WaitUntilTransactionInHeaviestState: timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// LatestBranchSlot latest time slot with some stateReaders
func (d *DAG) LatestBranchSlot() (ret ledger.Slot) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.latestBranchSlot
}

// ForEachVertexReadLocked Traversing all vertices. Beware: read-locked!
func (d *DAG) ForEachVertexReadLocked(fun func(vid *vertex.WrappedTx) bool) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	for _, vid := range d.vertices {
		if !fun(vid) {
			return
		}
	}
}

func (d *DAG) ParseMilestoneData(msVID *vertex.WrappedTx) (ret *txbuilder.MilestoneData) {
	msVID.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		ret = txbuilder.ParseMilestoneData(v.Tx.SequencerOutput().Output)
	}})
	return
}

// Vertices to avoid global lock while traversing all utangle
func (d *DAG) Vertices(filterByID ...func(txid *ledger.TransactionID) bool) []*vertex.WrappedTx {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	if len(filterByID) == 0 {
		return maps.Values(d.vertices)
	}
	return util.ValuesFiltered(d.vertices, func(vid *vertex.WrappedTx) bool {
		return filterByID[0](&vid.ID)
	})
}

func (d *DAG) VerticesDescending() []*vertex.WrappedTx {
	ret := d.Vertices()
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().After(ret[j].Timestamp())
	})
	return ret
}
