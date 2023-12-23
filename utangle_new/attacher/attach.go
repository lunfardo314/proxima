package attacher

import (
	"context"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle_new"
	"github.com/lunfardo314/proxima/utangle_new/vertex"
)

// attachTxID ensures the txid is on the utangle. Must be called from globally locked environment
func attachTxID(txid core.TransactionID, env AttachEnvironment, pullNonBranchIfNeeded bool) (vid *vertex.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			return
		}
		// it is new
		if !txid.IsBranchTransaction() {
			// if not branch -> just place the empty virtualTx on the utangle, no further action
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			if pullNonBranchIfNeeded {
				env.Pull(txid)
			}
			return
		}
		// it is a branch transaction. Look up for the corresponding state
		if bd, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid); branchAvailable {
			// corresponding state has been found, it is solid -> put virtual branch tx with the state reader
			vid = utangle_new.NewVirtualBranchTx(&bd).Wrap()
			env.AddVertexNoLock(vid)
			env.AddBranchNoLock(vid, &bd)
			vid.SetTxStatus(vertex.Good)
		} else {
			// the corresponding state is not in the multistate DB -> put virtualTx to the utangle -> pull it
			// the puller will trigger further solidification
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			env.Pull(txid) // always pull new branch. This will spin off sync process on the node
		}
	})
	return
}

// AttachTransaction attaches new incoming transaction. For sequencer transaction it starts attacher routine
// which manages solidification pull until transaction becomes solid or stopped by the context
func AttachTransaction(tx *transaction.Transaction, env AttachEnvironment, ctx context.Context) (vid *vertex.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		// look up for the txid
		vid = env.GetVertexNoLock(tx.ID())
		if vid == nil {
			// it is new. Create a new wrapped tx and put it to the utangle
			vid = vertex.New(tx).Wrap()
		} else {
			if !vid.IsVirtualTx() {
				return
			}
			// it is existing. Must virtualTx -> replace virtual tx with the full transaction
			vid.ConvertVirtualTxToVertex(vertex.New(tx))
		}
		env.AddVertexNoLock(vid)
		if vid.IsSequencerMilestone() {
			// starts attacher goroutine for each sequencer transaction
			go func() {
				status := runAttacher(vid, env, ctx)
				vid.SetTxStatus(status)
			}()
		}
	})
	return
}
