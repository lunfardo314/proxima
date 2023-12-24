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

func (ut *UTXOTangle) GetStateReaderForTheBranch(branch *vertex.WrappedTx) global.IndexedStateReader {
	util.Assertf(branch.IsBranchTransaction(), "branch.IsBranchTransaction()")
	return ut.MustGetIndexedStateReader(branch.ID())
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
