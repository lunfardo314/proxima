package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

// AttachTxID ensures the txid is on the MemDAG
// It load existing branches but does not pull anything
func AttachTxID(txid base.TransactionID, env Environment, opts ...AttachTxOption) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}

	{
		snapID := env.Branches().SnapshotBranchID()
		if txid.Slot() <= snapID.Slot()+3 {
			env.Tracef("nearSnapshot", ">>>> AttachTxID(snapSlot = %d): %s   %s", snapID.Slot(), txid.String(), txid.StringHex())
		}
	}

	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(txid)
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

	// new branch transaction. DB look-up is outside the global lock -> prevent congestion
	//branchData, branchAvailable := env.Branches().Get(txid)
	branchData, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid)

	env.WithGlobalWriteLock(func() {
		if vid = env.GetVertexNoLock(txid); vid != nil {
			return
		}
		if branchAvailable {
			// the corresponding state has been found, it is solid -> put virtual branch tx to the memDAG
			vid = vertex.WrapBranchDataAsVirtualTx(&branchData)
			env.AddVertexNoLock(vid)
			env.Assertf(vid.GetTxStatusNoLock() == vertex.Good, "vid.GetTxStatusNoLock()==vertex.Good")

			env.SendToTippool(vid)
			return
		}
		// the corresponding state is not in the multistate DB. Create virtual Tx for the ChainID
		vid = vertex.WrapTxID(txid)
		env.AddVertexNoLock(vid)
		vid.SetAttachmentDepthNoLock(options.depth)

		snapID := env.Branches().SnapshotBranchID()
		if txid.Slot() > snapID.Slot() {
			// the branch is definitely post-snapshot
			return
		}
		// check if the transaction is in the snapshot
		// edge case when the branch is before or at the snapshot baseline
		if env.Branches().GetStateReaderForTheBranch(snapID).KnowsCommittedTransaction(txid) {
			// it is in the snapshot state -> mark it GOOD branch
			vid.SetTxStatusGoodNoLock(nil, 0)
		} else {
			// it is not in the snapshot state -> mark it BAD branch
			err := fmt.Errorf("baseline branch state %s is before snapshot slot %d and is not available -> can't solidify baseline",
				txid.String(), snapID.Slot())
			vid.SetTxStatusBadNoLock(err)
		}
	})
	return
}

// AttachTransaction attaches the new incoming transaction. For sequencer transaction it starts the milestoneAttacher routine
// which manages solidification pulling until the transaction becomes solid or stopped by the context
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

	txid := tx.ID()
	vid = AttachTxID(txid, env, WithInvokedBy("addTx"))

	if env.Branches().TransactionIsInSnapshotState(txid) {
		// if the transaction is in the snapshot state, no need to start attacher, transaction can stay virtual
		return vid
	}

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

		// mark the vertex to prevent repetitive attachment
		vid.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted)

		// virtual tx is converted into full vertex with the full transaction
		env.Tracef(TraceTagAttach, ">>>>>>>>>>>>>>>>>>>>>>> ConvertVirtualTxToVertexNoLock: %s", tx.IDShortString())
		vid.ConvertVirtualTxToVertexNoLock(vertex.NewVertex(tx))

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
func InvalidateTxID(txid base.TransactionID, env Environment, reason error) {
	env.Tracef(TraceTagAttach, "InvalidateTxID: %s", txid.StringShort())

	vid := AttachTxID(txid, env, WithInvokedBy("InvalidateTxID"))
	vid.SetTxStatusBad(reason)
}

func AttachOutputID(oid base.OutputID, env Environment, opts ...AttachTxOption) vertex.WrappedOutput {
	return vertex.WrappedOutput{
		VID:   AttachTxID(oid.TransactionID(), env, opts...),
		Index: oid.Index(),
	}
}

func AttachOutputWithID(o ledger.OutputWithID, env Environment, opts ...AttachTxOption) (wOut vertex.WrappedOutput) {
	wOut = AttachOutputID(o.ID, env, opts...)
	wOut.VID.MustEnsureOutput(o.Output, o.ID.Index())
	return
}
