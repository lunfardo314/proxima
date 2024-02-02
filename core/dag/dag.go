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
		mutex      sync.RWMutex
		stateStore global.StateStore
		vertices   map[ledger.TransactionID]*vertex.WrappedTx
		branches   map[*vertex.WrappedTx]global.IndexedStateReader
	}
)

func New(stateStore global.StateStore) *DAG {
	return &DAG{
		stateStore: stateStore,
		vertices:   make(map[ledger.TransactionID]*vertex.WrappedTx),
		branches:   make(map[*vertex.WrappedTx]global.IndexedStateReader),
	}
}

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

func (d *DAG) AddVertexNoLock(vid *vertex.WrappedTx) {
	util.Assertf(d.GetVertexNoLock(&vid.ID) == nil, "d.GetVertexNoLock(vid.ID())==nil")
	d.vertices[vid.ID] = vid
}

const sharedStateReaderCacheSize = 3000

func (d *DAG) AddBranchNoLock(branchVID *vertex.WrappedTx) {
	util.Assertf(branchVID.IsBranchTransaction(), "branchVID.IsBranchTransaction()")

	if _, already := d.branches[branchVID]; !already {
		d.branches[branchVID] = d.MustGetIndexedStateReader(&branchVID.ID, sharedStateReaderCacheSize)
	}
}

// PurgeDeleted with global lock
func (d *DAG) PurgeDeleted(deleted []*vertex.WrappedTx) {
	d.WithGlobalWriteLock(func() {
		for _, vid := range deleted {
			delete(d.vertices, vid.ID)
		}
	})
}

func (d *DAG) PurgeBranches(deleted []*vertex.WrappedTx) {
	d.WithGlobalWriteLock(func() {
		for _, vid := range deleted {
			delete(d.branches, vid)
		}
	})
}

func (d *DAG) GetStateReaderForTheBranch(branchVID *vertex.WrappedTx) global.IndexedStateReader {
	util.Assertf(branchVID.IsBranchTransaction(), "branchVID.IsBranchTransaction()")

	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.branches[branchVID]
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
	slot := d.LatestBranchSlot()

	d.mutex.RLock()
	defer d.mutex.RUnlock()

	baseline := util.Maximum(d._branchesForSlot(slot), vertex.LessByCoverageAndID)
	return multistate.MakeSugared(d.branches[baseline]), baseline
}

func (d *DAG) HeaviestStateForLatestTimeSlot() multistate.SugaredStateReader {
	ret, _ := d.HeaviestStateForLatestTimeSlotWithBaseline()
	return ret
}

func (d *DAG) HeaviestBranchOfLatestTimeSlot() *vertex.WrappedTx {
	slot := d.LatestBranchSlot()

	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return util.Maximum(d._branchesForSlot(slot), vertex.LessByCoverageAndID)
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

func (d *DAG) _branchesForSlot(slot ledger.Slot) []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)
	for br := range d.branches {
		if br.Slot() == slot {
			ret = append(ret, br)
		}
	}
	return ret
}

func (d *DAG) _branchesDescending(slot ledger.Slot) []*vertex.WrappedTx {
	ret := d._branchesForSlot(slot)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].GetLedgerCoverage().Sum() > ret[j].GetLedgerCoverage().Sum()
	})
	return ret
}

// LatestBranchSlot latest time slot with some branches
func (d *DAG) LatestBranchSlot() (ret ledger.Slot) {
	m := util.Maximum(d.Branches(), func(vid1, vid2 *vertex.WrappedTx) bool {
		return vid1.Slot() < vid2.Slot()
	})
	if m == nil {
		return 0
	}
	return m.Slot()
}

func (d *DAG) _latestBranchSlot() (ret ledger.Slot) {
	for br := range d.branches {
		if br.Slot() > ret {
			ret = br.Slot()
		}
	}
	return
}

func (d *DAG) HasOutputInAllBranches(e ledger.Slot, oid *ledger.OutputID) bool {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	for _, br := range d._branchesDescending(e) {
		if !d.branches[br].HasUTXO(oid) {
			return false
		}
	}
	return true
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

// Branches to avoid global lock while traversing all utangle
func (d *DAG) Branches() []*vertex.WrappedTx {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return maps.Keys(d.branches)
}
