package dag

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/dag/vertex"
	"github.com/lunfardo314/proxima/global"
	"go.uber.org/zap"
)

type (
	UTXOTangleAccess interface {
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *core.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.StateStore
		GetStateReaderForTheBranch(branch *vertex.WrappedTx) global.IndexedStateReader
		AddBranch(branch *vertex.WrappedTx)
		EvidenceIncomingBranch(txid *core.TransactionID, seqID core.ChainID)
		EvidenceBookedBranch(txid *core.TransactionID, seqID core.ChainID)
	}

	PullEnvironment interface {
		Pull(txid core.TransactionID)
		OnChangeNotify(onChange, notify *vertex.WrappedTx)
		Notify(changed *vertex.WrappedTx)
	}

	AttachEnvironment interface {
		UTXOTangleAccess
		PullEnvironment
		Log() *zap.SugaredLogger
	}
)
