package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

const TraceTagBranchAvailable = "branchAvailable"

// AttachTxID ensures the txid is on the MemDAG
// It load existing branches but does not pull anything
func AttachTxID(txid ledger.TransactionID, env Environment, opts ...AttachTxOption) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}

	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			return
		}

		if options.depth > 0 && options.depth%100 == 0 {
			env.Log().Warnf("AttachTxID: solidification reached depth %d with %s", options.depth, txid.StringShort())
		}
		// it is new

		if !txid.IsBranchTransaction() {
			// if not branch -> just place the empty virtualTx on the utangle, no further action
			vid = vertex.WrapTxID(txid)
			vid.SetAttachmentDepthNoLock(options.depth)
			env.AddVertexNoLock(vid)
		}
	})
	if vid != nil {
		// already on the memDAG
		return
	}
	util.Assertf(txid.IsBranchTransaction(), "txid.IsBranchTransaction()")

	// new branch transaction. DB look up outside the global lock -> prevent congestion
	branchData, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid)

	env.WithGlobalWriteLock(func() {
		if vid = env.GetVertexNoLock(&txid); vid != nil {
			return
		}
		if branchAvailable {
			// corresponding state has been found, it is solid -> put virtual branch tx to the memDAG
			vid = vertex.WrapBranchDataAsVirtualTx(&branchData)
			env.AddVertexNoLock(vid)
			env.Assertf(vid.GetTxStatusNoLock() == vertex.Good, "vid.GetTxStatusNoLock()==vertex.Good")

			env.PostEventNewGood(vid)
			env.SendToTippool(vid)
			return
		}
		// the corresponding state is not in the multistate DB
		//   -> put virtualTx to the utangle
		//   -> pull is up to the attacher
		vid = vertex.WrapTxID(txid)
		env.AddVertexNoLock(vid)

		if txid.Timestamp().Before(env.SnapshotBranchID().Timestamp()) {
			// new branch is before the earliest slot in the state. Invalidate the transaction
			// This prevents from never ending solidification against wrong orphaned snapshot
			vid.SetTxStatusBad(fmt.Errorf("branch solidification error: transaction %s is before the snapshot slot %d", txid.StringShort(), env.SnapshotBranchID().Slot()))
			return
		}
		vid.SetAttachmentDepthNoLock(options.depth)
	})
	return
}

// AttachTransaction attaches new incoming transaction. For sequencer transaction it starts milestoneAttacher routine
// which manages solidification pulling until transaction becomes solid or stopped by the context
func AttachTransaction(tx *transaction.Transaction, env Environment, opts ...AttachTxOption) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}
	if options.enforceTimestamp {
		now := ledger.TimeNow()
		util.Assertf(!now.Before(tx.Timestamp()), "!now(%s).Before(tx.Timestamp())(%s)", now.String, tx.Timestamp().String)
	}
	env.Tracef(TraceTagAttach, "AttachTransaction: %s", tx.IDShortString)

	vid = AttachTxID(tx.ID(), env, WithInvokedBy("addTx"))

	vid.UnwrapVirtualTx(func(v *vertex.VirtualTransaction) {
		if vid.FlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted) {
			// case with already attached transaction
			if options.attachmentCallback != nil {
				go func() {
					//env.IncCounter("call")
					options.attachmentCallback(vid, vid.GetErrorNoLock())
					//env.DecCounter("call")
				}()
			}
			return
		}

		env.Tracef(TraceTagPull, "AttachTransaction %s. Since attachID: %v", tx.IDShortString, time.Since(v.Created))

		// mark the vertex in order to prevent repetitive attachment
		vid.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted)

		// virtual tx is converted into full vertex with the full transaction
		env.Tracef(TraceTagAttach, ">>>>>>>>>>>>>>>>>>>>>>> ConvertVirtualTxToVertexNoLock: %s", tx.IDShortString())
		vid.ConvertVirtualTxToVertexNoLock(vertex.New(tx))

		if vid.IsSequencerMilestone() {
			// for sequencer milestones start attacher
			metadata := options.metadata

			// start attacher routine
			go func() {
				env.IncCounter("att")
				defer env.DecCounter("att")

				env.MarkWorkProcessStarted(vid.IDShortString())
				runMilestoneAttacher(vid, metadata, options.attachmentCallback, env, options.ctx)
				env.MarkWorkProcessStopped(vid.IDShortString())

				if metadata != nil && metadata.TxBytesReceived != nil {
					env.AttachmentFinished(*metadata.TxBytesReceived)
				} else {
					env.AttachmentFinished()
				}
			}()
		}
		// significantly speeds up non-sequencer transactions
		if !vid.IsSequencerMilestone() || vid.IsBranchTransaction() {
			env.PokeAllWith(vid)
		}

		env.PostEventNewTransaction(vid)
	})
	return
}

// AttachTransactionFromBytes used for testing
func AttachTransactionFromBytes(txBytes []byte, env Environment, opts ...AttachTxOption) (*vertex.WrappedTx, error) {
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	if err != nil {
		return nil, err
	}
	return AttachTransaction(tx, env, opts...), nil
}

// InvalidateTxID marks existing vertex as BAD or creates new BAD
func InvalidateTxID(txid ledger.TransactionID, env Environment, reason error) *vertex.WrappedTx {
	env.Tracef(TraceTagAttach, "InvalidateTxID: %s", txid.StringShort())

	vid := AttachTxID(txid, env, WithInvokedBy("InvalidateTxID"))
	vid.SetTxStatusBad(reason)
	return vid
}

func AttachOutputID(oid ledger.OutputID, env Environment, opts ...AttachTxOption) vertex.WrappedOutput {
	return vertex.WrappedOutput{
		VID:   AttachTxID(oid.TransactionID(), env, opts...),
		Index: oid.Index(),
	}
}

func AttachOutputWithID(o *ledger.OutputWithID, env Environment, opts ...AttachTxOption) (vertex.WrappedOutput, error) {
	wOut := AttachOutputID(o.ID, env, opts...)
	if err := wOut.VID.EnsureOutputWithID(o); err != nil {
		return vertex.WrappedOutput{}, fmt.Errorf("cannot attach output %s: '%w'", o.ID.StringShort(), err)
	}
	return wOut, nil
}
