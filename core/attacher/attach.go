package attacher

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

// AttachTxID ensures the txid is on the MemDAG
func AttachTxID(txid ledger.TransactionID, env Environment, opts ...Option) (vid *vertex.WrappedTx) {
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
			// if not branch -> just place the empty virtualTx on the utangle_old, no further action
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			if options.pullNonBranch {
				env.Tracef(TraceTagAttach, "AttachTxID: pull new ID %s%s", txid.StringShort, by)
				env.TraceTx(&txid, "AttachTxID: pull new ID")
				env.Pull(txid)
			}
			return
		}
		// it is a branch transaction
		if options.doNotLoadBranch {
			// only needed ID (for call from the AttachTransaction)
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			return
		}
		// look up for the corresponding state
		if bd, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid); branchAvailable {
			// corresponding state has been found, it is solid -> put virtual branch tx with the state reader
			vid = vertex.NewVirtualBranchTx(&bd).WrapWithID(txid)
			vid.SetTxStatusGood()
			vid.SetLedgerCoverage(bd.LedgerCoverage)
			env.AddVertexNoLock(vid)
			env.PostEventNewGood(vid)
			env.SendToTippool(vid)
			env.Tracef(TraceTagAttach, "AttachTxID: branch fetched from the state: %s%s, coverage: %s",
				txid.StringShort, by, func() string { return util.GoTh(vid.GetLedgerCoverage()) })
			env.TraceTx(&txid, "AttachTxID: branch fetched from the state")

		} else {
			// the corresponding state is not in the multistate DB -> put virtualTx to the utangle_old -> pull it
			// the puller will trigger further solidification
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			env.Pull(txid) // always pull new branch. This will spin off sync process on the node
			env.Tracef(TraceTagAttach, "AttachTxID: added new branch vertex and pulled %s%s", txid.StringShort(), by)
			env.TraceTx(&txid, "AttachTxID: added new branch vertex and pulled")
		}
	})
	return
}

// InvalidateTxID marks existing vertex as BAD or creates new BAD
func InvalidateTxID(txid ledger.TransactionID, env Environment, reason error) *vertex.WrappedTx {
	env.Tracef(TraceTagAttach, "InvalidateTxID: %s", txid.StringShort())
	env.TraceTx(&txid, "InvalidateTxID")

	vid := AttachTxID(txid, env, OptionDoNotLoadBranch, OptionInvokedBy("InvalidateTxID"))
	vid.SetTxStatusBad(reason)
	return vid
}

func AttachOutputID(oid ledger.OutputID, env Environment, opts ...Option) vertex.WrappedOutput {
	env.TraceTx(util.Ref(oid.TransactionID()), "AttachOutputID: #%d", oid.Index())
	return vertex.WrappedOutput{
		VID:   AttachTxID(oid.TransactionID(), env, opts...),
		Index: oid.Index(),
	}
}

// AttachTransaction attaches new incoming transaction. For sequencer transaction it starts milestoneAttacher routine
// which manages solidification pulling until transaction becomes solid or stopped by the context
func AttachTransaction(tx *transaction.Transaction, env Environment, opts ...Option) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}
	if options.enforceTimestamp {
		now := ledger.TimeNow()
		util.Assertf(!now.Before(tx.Timestamp()),
			"!ledger.TimeNow().Before(tx.Timestamp()) %s -- %s",
			now.String(), tx.Timestamp().String())
	}

	env.Tracef(TraceTagAttach, "AttachTransaction: %s", tx.IDShortString)
	env.TraceTx(tx.ID(), "AttachTransaction")

	vid = AttachTxID(*tx.ID(), env, OptionDoNotLoadBranch, OptionInvokedBy("addTx"))
	vid.Unwrap(vertex.UnwrapOptions{
		// full vertex or with attachment process already invoked will be ignored
		VirtualTx: func(v *vertex.VirtualTransaction) {
			if vid.FlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted) {
				return
			}
			// mark the vertex in order to prevent repetitive attachment
			vid.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted)

			if options.metadata != nil && options.metadata.SourceTypeNonPersistent == txmetadata.SourceTypeTxStore {
				// prevent persisting transaction bytes twice
				vid.SetFlagsUpNoLock(vertex.FlagVertexTxBytesPersisted)
			}

			// virtual tx is converted into full vertex with the full transaction
			env.Tracef(TraceTagAttach, ">>>>>>>>>>>>>>>>>>>>>>> ConvertVirtualTxToVertexNoLock: %s", tx.IDShortString())
			vid.ConvertVirtualTxToVertexNoLock(vertex.New(tx))

			if vid.IsSequencerMilestone() {
				// for sequencer milestones start attacher
				callback := options.attachmentCallback
				if callback == nil {
					callback = func(_ *vertex.WrappedTx, _ error) {}
				}
				metadata := options.metadata

				go func() {
					env.MarkWorkProcessStarted(vid.IDShortString())
					env.TraceTx(&vid.ID, "runMilestoneAttacher: start")
					runMilestoneAttacher(vid, metadata, callback, env)
					env.TraceTx(&vid.ID, "runMilestoneAttacher: exit")
					env.MarkWorkProcessStopped(vid.IDShortString())
				}()
			} else {
				// pull predecessors of non-sequencer transactions which are on the same slot.
				// We limit pull to the same slot so that not to fall into the endless pull cycle
				slot := vid.Slot()
				tx.PredecessorTransactionIDs().ForEach(func(txid ledger.TransactionID) bool {
					if txid.Slot() == slot {
						AttachTxID(txid, env, OptionPullNonBranch)
					}
					return true
				})
				vid.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentFinished)
				// notify others who are waiting for the vid
				env.PokeAllWith(vid)
			}

			env.PostEventNewTransaction(vid)
		},
	})
	return
}

// AttachTransactionFromBytes used for testing
func AttachTransactionFromBytes(txBytes []byte, env Environment, opts ...Option) (*vertex.WrappedTx, error) {
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	if err != nil {
		return nil, err
	}
	return AttachTransaction(tx, env, opts...), nil
}

const maxTimeout = 10 * time.Minute

func EnsureBranch(txid ledger.TransactionID, env Environment, timeout ...time.Duration) (*vertex.WrappedTx, error) {
	vid := AttachTxID(txid, env)
	to := maxTimeout
	if len(timeout) > 0 {
		to = timeout[0]
	}
	deadline := time.Now().Add(to)
	for vid.GetTxStatus() == vertex.Undefined {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("failed to fetch branch %s in %v", txid.StringShort(), to)
		}
		time.Sleep(10 * time.Millisecond)
	}
	env.Tracef(TraceTagEnsureLatestBranches, "ensure branch %s", txid.StringShort())
	return vid, nil
}

func MustEnsureBranch(txid ledger.TransactionID, env Environment, timeout ...time.Duration) *vertex.WrappedTx {
	ret, err := EnsureBranch(txid, env, timeout...)
	env.AssertNoError(err)
	return ret
}

const TraceTagEnsureLatestBranches = "latest"

func EnsureLatestBranches(env Environment) error {
	branchTxIDs := multistate.FetchLatestBranchTransactionIDs(env.StateStore())
	for _, branchID := range branchTxIDs {
		if _, err := EnsureBranch(branchID, env); err != nil {
			return err
		}
	}
	return nil
}
