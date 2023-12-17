package attacher

import (
	"context"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle_new"
)

// _attachTxID ensures the txid is on the utangle. Must be called from globally locked environment
func _attachTxID(txid core.TransactionID, env AttachEnvironment) (vid *utangle_new.WrappedTx) {
	vid = env.GetVertexNoLock(&txid)
	if vid != nil {
		// found existing -> return it
		return
	}
	// it is new
	if !txid.IsBranchTransaction() {
		// if not branch -> just place the empty virtualTx on the utangle, no further action
		vid = utangle_new.WrapTxID(txid)
		env.AddVertexNoLock(vid)
		return
	}
	// it is a branch transaction. Look up for the corresponding state
	if bd, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid); !branchAvailable {
		// the corresponding state is not in the multistate DB -> put virtualTx to the utangle -> pull it
		// the puller will trigger further solidification
		vid = utangle_new.WrapTxID(txid)
		env.AddVertexNoLock(vid)
		env.Pull(txid)
	} else {
		// corresponding state has been found, it is solid -> put virtual branch tx with the state reader
		vid = utangle_new.NewVirtualBranchTx(&bd).Wrap()
		env.AddVertexNoLock(vid)
		rdr := multistate.MustNewReadable(env.StateStore(), bd.Root, maxStateReaderCacheSize)
		vid.SetBaselineStateReader(rdr)
	}
	return
}

// AttachInput attaches transaction and links consumer with the transaction.
// Returns vid of the consumed transaction, or nil if input index is wrong
func AttachInput(consumer *utangle_new.WrappedTx, inputIdx byte, env AttachEnvironment) (vid *utangle_new.WrappedTx) {
	var inOid core.OutputID
	var err error
	consumer.Unwrap(utangle_new.UnwrapOptions{
		Vertex: func(v *utangle_new.Vertex) {
			inOid, err = v.Tx.InputAt(inputIdx)
		},
		VirtualTx: vid.PanicShouldNotBeVirtualTx,
		Deleted:   vid.PanicAccessDeleted,
	})
	if err != nil {
		return nil
	}
	env.WithGlobalWriteLock(func() {
		vid = _attachTxID(inOid.TransactionID(), env)
		// attach and propagate new conflict set, if any
		if !vid.AttachConsumerNoLock(inOid.Index(), consumer) {
			// failed to attach consumer
			vid = nil
		}
	})
	return
}

func AttachTxID(txid core.TransactionID, env AttachEnvironment) (vid *utangle_new.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		vid = _attachTxID(txid, env)
	})
	return
}

// AttachTransaction attaches new incoming transaction. For sequencer transaction it starts attacher routine
// which manages solidification pull until transaction becomes solid or stopped by the context
func AttachTransaction(tx *transaction.Transaction, env AttachEnvironment, ctx context.Context) (vid *utangle_new.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		// look up for the txid
		vid = env.GetVertexNoLock(tx.ID())
		if vid == nil {
			// it is new. Create a new wrapped tx and put it to the utangle
			vid = utangle_new.NewVertex(tx).Wrap()
		} else {
			// it is existing. Must virtualTx -> replace virtual tx with the full transaction
			vid.MustConvertVirtualTxToVertex(utangle_new.NewVertex(tx))
		}
		env.AddVertexNoLock(vid)
		if vid.IsSequencerMilestone() {
			// starts attacher goroutine for sequencer transactions.
			go func() {
				newAttacher(vid, env, ctx).
					run().
					close()
			}()
		}
	})
	return
}
