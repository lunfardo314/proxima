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
	//env.TraceTx(&txid, "AttachTxID")

	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}

	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			//env.Tracef(TraceTagAttach, "AttachTxID: found existing %s%s", txid.StringShort, by)
			//env.TraceTx(&txid, "AttachTxID: found existing")
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
		if vid.IDHasFragment("007d5b335") {
			env.Log().Infof("@@>> attach tx ID %s", vid.IDShortString())
		}

		// already on the memDAG
		return
	}
	util.Assertf(txid.IsBranchTransaction(), "txid.IsBranchTransaction()")

	// new branch transaction. DB look up outside the global lock -> prevent congestion
	branchData, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid)
	//if branchAvailable {
	//	env.Log().Infof("$$$$$$ branch available 1: %s", txid.StringShort())
	//} else {
	//	env.Log().Infof("$$$$$$ branch is NOT available 1: %s", txid.StringShort())
	//}

	env.WithGlobalWriteLock(func() {
		if vid = env.GetVertexNoLock(&txid); vid != nil {
			return
		}
		if branchAvailable {
			env.Log().Infof("$$$$$$ branch available 2: %s", txid.StringShort())
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
	env.TraceTx(tx.ID(), "AttachTransaction")

	vid = AttachTxID(*tx.ID(), env, WithInvokedBy("addTx"))

	if vid.IDHasFragment("007d5b335") {
		env.Log().Infof("@@>> attach transaction %s", vid.IDShortString())
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

		// mark the vertex in order to prevent repetitive attachment
		vid.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted)

		// virtual tx is converted into full vertex with the full transaction
		env.Tracef(TraceTagAttach, ">>>>>>>>>>>>>>>>>>>>>>> ConvertVirtualTxToVertexNoLock: %s", tx.IDShortString())
		vid.ConvertVirtualTxToVertexNoLock(vertex.New(tx))

		if vid.IsSequencerMilestone() {
			// for sequencer milestones start attacher
			metadata := options.metadata

			//if vid.IDHasFragment("00e5c36923bc") {
			//	env.Log().Infof(">>>>>>>> attachTransaction %s before run attacher", vid.IDShortString())
			//}
			if vid.Slot() <= 125420 {
				env.Log().Infof("~~~~~~~~~ %s    %s", vid.IDShortString(), vid.ID.StringHex())
			}

			// start attacher routine
			go func() {
				env.IncCounter("att")
				defer env.DecCounter("att")

				env.MarkWorkProcessStarted(vid.IDShortString())
				env.TraceTx(&vid.ID, "runMilestoneAttacher: start")

				runMilestoneAttacher(vid, metadata, options.attachmentCallback, env, options.ctx)

				env.TraceTx(&vid.ID, "runMilestoneAttacher: exit")
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
	env.TraceTx(&txid, "InvalidateTxID")

	vid := AttachTxID(txid, env, WithInvokedBy("InvalidateTxID"))
	vid.SetTxStatusBad(reason)
	return vid
}

func AttachOutputID(oid ledger.OutputID, env Environment, opts ...AttachTxOption) vertex.WrappedOutput {
	env.TraceTx(util.Ref(oid.TransactionID()), "AttachOutputID: #%d", oid.Index())
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
