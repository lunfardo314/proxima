package attacher

import (
	"context"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

// AttachTxID ensures the txid is on the utangle. Must be called from globally locked environment
func AttachTxID(txid ledger.TransactionID, env Environment, opts ...Option) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}

	by := ""
	if options.calledBy != "" {
		by = " by " + options.calledBy
	}
	env.Tracef(TraceTagAttach, "AttachTxID: %s%s", txid.StringShort(), by)
	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			env.Tracef(TraceTagAttach, "AttachTxID: found existing %s%s", txid.StringShort(), by)
			return
		}
		// it is new
		if !txid.IsBranchTransaction() {
			// if not branch -> just place the empty virtualTx on the utangle_old, no further action
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			if options.pullNonBranch {
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
			env.AddBranchNoLock(vid) // <<<< will be reading branch data twice. Not big problem
			env.PostEventNewGood(vid)
			env.Tracef(TraceTagAttach, "AttachTxID: branch fetched from the state: %s%s", txid.StringShort(), by)
		} else {
			// the corresponding state is not in the multistate DB -> put virtualTx to the utangle_old -> pull it
			// the puller will trigger further solidification
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			env.Pull(txid) // always pull new branch. This will spin off sync process on the node
			env.Tracef(TraceTagAttach, "AttachTxID: added new branch vertex and pulled %s%s", txid.StringShort(), by)
		}
	})
	return
}

// InvalidateTxID marks existing vertex as BAD or creates new BAD
func InvalidateTxID(txid ledger.TransactionID, env Environment, reason error) *vertex.WrappedTx {
	env.Tracef(TraceTagAttach, "InvalidateTxID: %s", txid.StringShort())

	vid := AttachTxID(txid, env, OptionDoNotLoadBranch, OptionInvokedBy("InvalidateTxID"))
	vid.SetTxStatusBad(reason)
	return vid
}

func AttachOutputID(oid ledger.OutputID, env Environment, opts ...Option) vertex.WrappedOutput {
	return vertex.WrappedOutput{
		VID:   AttachTxID(oid.TransactionID(), env, opts...),
		Index: oid.Index(),
	}
}

// AttachTransaction attaches new incoming transaction. For sequencer transaction it starts milestoneAttacher routine
// which manages solidification pull until transaction becomes solid or stopped by the context
func AttachTransaction(tx *transaction.Transaction, env Environment, opts ...Option) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}
	env.Tracef(TraceTagAttach, "AttachTransaction: %s", tx.IDShortString)

	if tx.IsBranchTransaction() {
		env.EvidenceIncomingBranch(tx.ID(), tx.SequencerTransactionData().SequencerID)
	}
	vid = AttachTxID(*tx.ID(), env, OptionDoNotLoadBranch, OptionInvokedBy("addTx"))
	if vid.IsBadOrDeleted() {
		// if txid was invalidated once, it will always return the same bad transaction
		return vid
	}

	vid.Unwrap(vertex.UnwrapOptions{
		// full vertex will be ignored
		// virtual tx will be converted into full vertex and milestoneAttacher started, if necessary
		VirtualTx: func(v *vertex.VirtualTransaction) {
			fullVertex := vertex.New(tx)
			vid.ConvertVirtualTxToVertexNoLock(fullVertex)
			env.Tracef(TraceTagAttach, "converted to vertex %s", tx.IDShortString)

			if options.metadata != nil && options.metadata.SourceTypeNonPersistent == txmetadata.SourceTypeTxStore {
				// prevent from persisting twice
				vid.SetFlagsUpNoLock(vertex.FlagTxBytesPersisted)
			}

			env.Tracef(TraceTagAttach, "post new tx event %s", tx.IDShortString)
			env.PostEventNewTransaction(vid)

			if !vid.IsSequencerMilestone() {
				// pull non-attached for non-sequencer transactions, which are on the same slot
				// We limit pull to one slot back in order not to fall into the endless pull cycle
				tx.PredecessorTransactionIDs().ForEach(func(txid ledger.TransactionID) bool {
					if txid.Slot() == vid.Slot() {
						AttachTxID(txid, env).Unwrap(vertex.UnwrapOptions{VirtualTx: func(vInput *vertex.VirtualTransaction) {
							if !txid.IsBranchTransaction() {
								env.Pull(txid)
							}
						}})
					}
					return true
				})
				env.PokeAllWith(vid)
				return
			}

			// starts milestoneAttacher goroutine for sequencer transaction
			ctx := options.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			callback := options.attachmentCallback
			if callback == nil {
				callback = func(_ *vertex.WrappedTx, _ error) {}
			}

			runFun := func() {
				status, stats, err := runMilestoneAttacher(vid, options.metadata, env, ctx)
				switch status {
				case vertex.Good:
					vid.SetTxStatusGood()
				case vertex.Bad:
					vid.SetTxStatusBad(err)
				default:
					env.Log().Fatalf("AttachTransaction: Internal error: bad status")
				}

				msData := env.ParseMilestoneData(vid)
				env.Log().Info(logFinalStatusString(vid, stats, msData))
				env.PokeAllWith(vid)

				// calling callback with timeout in order to detect wrong callbacks immediately
				ok := util.CallWithTimeout(func() {
					callback(vid, err)
				}, 10*time.Millisecond)
				if !ok {
					env.Log().Fatalf("AttachTransaction: Internal error: 10 milisec timeout exceeded while calling callback")
				}
			}

			// if debuggerFriendly == true, the panic is not caught, so it is more convenient in the debugger
			const debuggerFriendly = true
			if debuggerFriendly {
				go runFun()
			} else {
				util.RunWrappedRoutine(vid.IDShortString(), runFun, func(err error) {
					env.Log().Fatalf("uncaught exception in %s: '%v'", vid.IDShortString(), err)
				}, common.ErrDBUnavailable)
			}
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
	util.AssertNoError(err)
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
