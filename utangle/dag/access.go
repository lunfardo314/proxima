package dag

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
)

func (ut *DAG) StateStore() global.StateStore {
	return ut.stateStore
}

func (ut *DAG) WithGlobalWriteLock(fun func()) {
	ut.mutex.Lock()
	fun()
	ut.mutex.Unlock()
}

func (ut *DAG) GetVertexNoLock(txid *core.TransactionID) *vertex.WrappedTx {
	return ut.vertices[*txid]
}

func (ut *DAG) AddVertexNoLock(vid *vertex.WrappedTx) {
	util.Assertf(ut.GetVertexNoLock(vid.ID()) == nil, "ut.GetVertexNoLock(vid.ID())==nil")
	ut.vertices[*vid.ID()] = vid
}

const sharedStateReaderCacheSize = 3000

func (ut *DAG) AddBranchNoLock(branchVID *vertex.WrappedTx) {
	util.Assertf(branchVID.IsBranchTransaction(), "branchVID.IsBranchTransaction()")
	util.Assertf(branchVID.GetTxStatus() != vertex.Bad, "branchVID.GetTxStatus() != vertex.Bad")

	_, already := ut.branches[branchVID]
	util.Assertf(!already, "repeating branch %s", branchVID.IDShortString())

	ut.branches[branchVID] = ut.MustGetIndexedStateReader(branchVID.ID(), sharedStateReaderCacheSize)
}

func (ut *DAG) GetStateReaderForTheBranch(branchVID *vertex.WrappedTx) global.IndexedStateReader {
	util.Assertf(branchVID.IsBranchTransaction(), "branchVID.IsBranchTransaction()")
	util.Assertf(branchVID.GetTxStatus() == vertex.Good, "branchVID.GetTxStatus()==vertex.Good")

	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return ut.branches[branchVID]
}

func (ut *DAG) EvidenceIncomingBranch(txid *core.TransactionID, seqID core.ChainID) {
	ut.syncData.EvidenceIncomingBranch(txid, seqID)
}

func (ut *DAG) EvidenceBookedBranch(txid *core.TransactionID, seqID core.ChainID) {
	ut.syncData.EvidenceBookedBranch(txid, seqID)
}
