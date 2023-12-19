package attacher

import (
	"context"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle_new"
	"github.com/lunfardo314/proxima/utangle_new/vertex"
)

// _attachTxID ensures the txid is on the utangle. Must be called from globally locked environment
func _attachTxID(txid core.TransactionID, env AttachEnvironment, pullNonBranchIfNeeded bool) (vid *vertex.WrappedTx) {
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
		vid.SetTxStatus(vertex.TxStatusGood)
	} else {
		// the corresponding state is not in the multistate DB -> put virtualTx to the utangle -> pull it
		// the puller will trigger further solidification
		vid = vertex.WrapTxID(txid)
		env.AddVertexNoLock(vid)
		env.Pull(txid) // always pull new branch. This will spin off sync process on the node
	}
	return
}

func AttachTxID(txid core.TransactionID, env AttachEnvironment, pullNonBranchIfNeeded bool) (vid *vertex.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		vid = _attachTxID(txid, env, pullNonBranchIfNeeded)
	})
	return
}

// AttachInput attaches transaction and links consumer with the transaction.
// Returns vid of the consumed transaction, or nil if input index is wrong
func AttachInput(consumer *vertex.WrappedTx, inputIdx byte, env AttachEnvironment, baselineStateReader *multistate.SugaredStateReader, pullIfNeeded bool) (vid *vertex.WrappedTx) {
	var inOid core.OutputID
	var out *core.Output
	var err error
	consumer.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			inOid, err = v.Tx.InputAt(inputIdx)
		},
		VirtualTx: vid.PanicShouldNotBeVirtualTx,
		Deleted:   vid.PanicAccessDeleted,
	})
	if err != nil {
		return nil
	}

	if baselineStateReader != nil {
		inTxID := inOid.TransactionID()
		if baselineStateReader.KnowsCommittedTransaction(&inTxID) {
			if out = baselineStateReader.GetOutput(&inOid); out == nil {
				// invalid input
				return nil
			}
		}
		// input tx not known to the state
	}
	env.WithGlobalWriteLock(func() {
		vid = _attachTxID(inOid.TransactionID(), env, pullIfNeeded && out == nil)
		if out != nil && !vid.EnsureOutput(inOid.Index(), out) {
			// wrong output index
			vid = nil
			return
		}
		// attach and propagate new conflict set, if any
		if !vid.AttachConsumerNoLock(inOid.Index(), consumer) {
			// failed to attach consumer
			vid = nil
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
			vid = utangle_new.NewVertex(tx).Wrap()
		} else {
			if !vid.IsVirtualTx() {
				return
			}
			// it is existing. Must virtualTx -> replace virtual tx with the full transaction
			vid.ConvertVirtualTxToVertex(utangle_new.NewVertex(tx))
		}
		env.AddVertexNoLock(vid)
		if vid.IsSequencerMilestone() {
			// starts attacher goroutine for sequencer transactions.
			go runAttacher(vid, env, ctx)
		}
	})
	return
}
