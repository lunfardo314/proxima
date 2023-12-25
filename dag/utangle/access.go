package utangle

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/dag/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap"
)

func (ut *UTXOTangle) Log() *zap.SugaredLogger {
	//TODO implement me
	panic("implement me")
}

func (ut *UTXOTangle) StateStore() global.StateStore {
	return ut.stateStore
}

func (ut *UTXOTangle) WithGlobalWriteLock(fun func()) {
	ut.mutex.Lock()
	fun()
	ut.mutex.Unlock()
}

func (ut *UTXOTangle) GetVertexNoLock(txid *core.TransactionID) *vertex.WrappedTx {
	return ut.vertices[*txid]
}

func (ut *UTXOTangle) AddVertexNoLock(vid *vertex.WrappedTx) {
	util.Assertf(ut.GetVertexNoLock(vid.ID()) == nil, "ut.GetVertexNoLock(vid.ID())==nil")
	ut.vertices[*vid.ID()] = vid
}

const sharedStateReaderCacheSize = 3000

func (ut *UTXOTangle) AddBranch(branchVID *vertex.WrappedTx) {
	util.Assertf(branchVID.IsBranchTransaction(), "branchVID.IsBranchTransaction()")
	util.Assertf(branchVID.GetTxStatus() == vertex.Good, "branchVID.GetTxStatus()==vertex.Good")

	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	_, already := ut.branches[branchVID]
	util.Assertf(!already, "repeating branch %s", branchVID.IDShortString())

	ut.branches[branchVID] = ut.MustGetIndexedStateReader(branchVID.ID(), sharedStateReaderCacheSize)
}

func (ut *UTXOTangle) GetStateReaderForTheBranch(branchVID *vertex.WrappedTx) global.IndexedStateReader {
	util.Assertf(branchVID.IsBranchTransaction(), "branchVID.IsBranchTransaction()")
	util.Assertf(branchVID.GetTxStatus() == vertex.Good, "branchVID.GetTxStatus()==vertex.Good")

	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return ut.branches[branchVID]
}

func (ut *UTXOTangle) Pull(txid core.TransactionID) {
	//TODO implement me
	panic("implement me")
}

func (ut *UTXOTangle) OnChangeNotify(onChange, notify *vertex.WrappedTx) {
	//TODO implement me
	panic("implement me")
}

func (ut *UTXOTangle) Notify(changed *vertex.WrappedTx) {
	//TODO implement me
	panic("implement me")
}

func (ut *UTXOTangle) EvidenceIncomingBranch(txid *core.TransactionID, seqID core.ChainID) {
	ut.syncData.EvidenceIncomingBranch(txid, seqID)
}

func (ut *UTXOTangle) EvidenceBookedBranch(txid *core.TransactionID, seqID core.ChainID) {
	ut.syncData.EvidenceBookedBranch(txid, seqID)
}
