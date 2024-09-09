package attacher

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

// AttachTxID ensures the txid is on the MemDAG
// It load existing branches but does not pull anything
func AttachTxID(txid ledger.TransactionID, env Environment, opts ...AttachTxOption) (vid *vertex.WrappedTx) {
	env.TraceTx(&txid, "AttachTxID")

	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}

	by := ""
	if options.calledBy != "" {
		by = " by " + options.calledBy
	}
	env.Tracef(TraceTagAttach, "AttachTxID: %s%s", txid.StringShort, by)

	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			env.Tracef(TraceTagAttach, "AttachTxID: found existing %s%s", txid.StringShort, by)
			env.TraceTx(&txid, "AttachTxID: found existing")
			return
		}
		env.Tracef(TraceTagAttach, "AttachTxID: new ID %s%s", txid.StringShort, by)
		env.TraceTx(&txid, "AttachTxID: new ID")

		// it is new
		if !txid.IsBranchTransaction() {
			// if not branch -> just place the empty virtualTx on the utangle, no further action
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			return
		}
		// it is a branch transaction
		// look up for the corresponding state
		if branchData, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid); branchAvailable {
			// corresponding state has been found, it is solid -> put virtual branch tx to the memDAG
			vid = vertex.WrapBranchDataAsVirtualTx(&branchData)
			env.AddVertexNoLock(vid)
			env.PostEventNewGood(vid)
			env.SendToTippool(vid)

			env.Tracef(TraceTagAttach, "AttachTxID: branch fetched from the state: %s%s, accumulatedCoverage: %s",
				txid.StringShort, by, func() string { return util.Th(vid.GetLedgerCoverage()) })
			env.TraceTx(&txid, "AttachTxID: branch fetched from the state")

		} else {
			// the corresponding state is not in the multistate DB -> put virtualTx to the utangle -> pull is up to attacher
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)

			env.Tracef(TraceTagAttach, "AttachTxID: added new branch vertex and pulled %s%s", txid.StringShort(), by)
			env.TraceTx(&txid, "AttachTxID: added new branch vertex and pulled")
		}
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

	vid = AttachTxID(*tx.ID(), env, OptionInvokedBy("addTx"))
	vid.UnwrapVirtualTx(func(v *vertex.VirtualTransaction) {
		if vid.FlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted | vertex.FlagVertexTxAttachmentFinished) {
			// case with already attached transaction
			if options.attachmentCallback != nil {
				go options.attachmentCallback(vid, vid.GetErrorNoLock())
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
				env.MarkWorkProcessStarted(vid.IDShortString())
				env.TraceTx(&vid.ID, "runMilestoneAttacher: start")
				env.IncAttacherCounter()

				runMilestoneAttacher(vid, metadata, options.attachmentCallback, env, options.ctx)

				env.TraceTx(&vid.ID, "runMilestoneAttacher: exit")
				env.MarkWorkProcessStopped(vid.IDShortString())
				env.DecAttacherCounter()
			}()
		}
		// significantly speed up non-sequencer transactions
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

	vid := AttachTxID(txid, env, OptionInvokedBy("InvalidateTxID"))
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
