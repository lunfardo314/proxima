package dag

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
)

func (d *DAG) StateStore() global.StateStore {
	return d.stateStore
}

func (d *DAG) WithGlobalWriteLock(fun func()) {
	d.mutex.Lock()
	fun()
	d.mutex.Unlock()
}

func (d *DAG) GetVertexNoLock(txid *core.TransactionID) *vertex.WrappedTx {
	return d.vertices[*txid]
}

func (d *DAG) AddVertexNoLock(vid *vertex.WrappedTx) {
	util.Assertf(d.GetVertexNoLock(&vid.ID) == nil, "d.GetVertexNoLock(vid.ID())==nil")
	d.vertices[vid.ID] = vid
}

const sharedStateReaderCacheSize = 3000

func (d *DAG) AddBranchNoLock(branchVID *vertex.WrappedTx) {
	util.Assertf(branchVID.IsBranchTransaction(), "branchVID.IsBranchTransaction()")
	util.Assertf(branchVID.GetTxStatus() != vertex.Bad, "branchVID.GetTxStatus() != vertex.Bad")

	_, already := d.branches[branchVID]
	util.Assertf(!already, "repeating branch %s", branchVID.IDShortString())

	d.branches[branchVID] = d.MustGetIndexedStateReader(&branchVID.ID, sharedStateReaderCacheSize)
}

func (d *DAG) GetStateReaderForTheBranch(branchVID *vertex.WrappedTx) global.IndexedStateReader {
	util.Assertf(branchVID.IsBranchTransaction(), "branchVID.IsBranchTransaction()")

	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.branches[branchVID]
}

func (d *DAG) EvidenceIncomingBranch(txid *core.TransactionID, seqID core.ChainID) {
	d.syncData.EvidenceIncomingBranch(txid, seqID)
}

func (d *DAG) EvidenceBookedBranch(txid *core.TransactionID, seqID core.ChainID) {
	d.syncData.EvidenceBookedBranch(txid, seqID)
}

func (d *DAG) GetIndexedStateReader(branchTxID *core.TransactionID, clearCacheAtSize ...int) (global.IndexedStateReader, error) {
	rr, found := multistate.FetchRootRecord(d.stateStore, *branchTxID)
	if !found {
		return nil, fmt.Errorf("root record for %s has not been found", branchTxID.StringShort())
	}
	return multistate.NewReadable(d.stateStore, rr.Root, clearCacheAtSize...)
}

func (d *DAG) MustGetIndexedStateReader(branchTxID *core.TransactionID, clearCacheAtSize ...int) global.IndexedStateReader {
	ret, err := d.GetIndexedStateReader(branchTxID, clearCacheAtSize...)
	util.AssertNoError(err)
	return ret
}

func (d *DAG) HeaviestStateForLatestTimeSlot() global.IndexedStateReader {
	slot := d.LatestBranchSlot()

	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.branches[util.Maximum(d._branchesForSlot(slot), vertex.Less)]
}

func (d *DAG) GetVertex(txid *core.TransactionID) *vertex.WrappedTx {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.GetVertexNoLock(txid)
}

func (d *DAG) _branchesForSlot(slot core.TimeSlot) []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)
	for br := range d.branches {
		if br.TimeSlot() == slot {
			ret = append(ret, br)
		}
	}
	return ret
}

func (d *DAG) _branchesDescending(slot core.TimeSlot) []*vertex.WrappedTx {
	ret := d._branchesForSlot(slot)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].GetLedgerCoverage().Sum() > ret[j].GetLedgerCoverage().Sum()
	})
	return ret
}

// LatestBranchSlot latest time slot with some branches
func (d *DAG) LatestBranchSlot() (ret core.TimeSlot) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d._latestBranchSlot()
}

func (d *DAG) _latestBranchSlot() (ret core.TimeSlot) {
	for br := range d.branches {
		if br.TimeSlot() > ret {
			ret = br.TimeSlot()
		}
	}
	return
}

func (d *DAG) FindOutputInLatestTimeSlot(oid *core.OutputID) (ret *vertex.WrappedTx, rdr multistate.SugaredStateReader) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	for _, br := range d._branchesDescending(d._latestBranchSlot()) {
		if d.branches[br].HasUTXO(oid) {
			return br, multistate.MakeSugared(d.branches[br])
		}
	}
	return
}

func (d *DAG) HasOutputInAllBranches(e core.TimeSlot, oid *core.OutputID) bool {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	for _, br := range d._branchesDescending(e) {
		if !d.branches[br].HasUTXO(oid) {
			return false
		}
	}
	return true
}

// ForEachVertex mostly for testing
func (d *DAG) ForEachVertex(fun func(vid *vertex.WrappedTx) bool) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	for _, vid := range d.vertices {
		if !fun(vid) {
			return
		}
	}
}
