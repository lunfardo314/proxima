package attacher

import (
	"context"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle_new"
)

// AttachTxID ensures the txid is on the utangle
func AttachTxID(txid core.TransactionID, env AttachEnvironment) (vid *utangle_new.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			return
		}
		// it is new
		if !txid.BranchFlagON() {
			// if not branch -> just place the virtualTx on the utangle, no further action
			vid = utangle_new.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			return
		}
		// it is a branch transaction. Look up for the corresponding state
		bd, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid)
		if !branchAvailable {
			// the corresponding state is not in the multistate DB -> put virtualTx to the utangle -> pull it
			// the puller will trigger further solidification
			vid = utangle_new.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			env.Pull(txid)
			return
		}
		// corresponding state has been found, it is solid -> put virtual branch tx with the state reader
		vid = utangle_new.NewVirtualBranchTx(&bd).Wrap()
		env.AddVertexNoLock(vid)
		rdr := multistate.MustNewReadable(env.StateStore(), bd.Root, maxStateReaderCacheSize)
		vid.SetBaselineStateReader(rdr)
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
				newAttacher(vid, env, ctx).run()
			}()
		}
	})
	return
}
