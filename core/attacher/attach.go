package attacher

import (
	"context"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

// AttachTxID ensures the txid is on the utangle_old. Must be called from globally locked environment
func AttachTxID(txid ledger.TransactionID, env AttachEnvironment, opts ...Option) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}

	by := ""
	if options.calledBy != "" {
		by = " by " + options.calledBy
	}
	tracef(env, "AttachTxID: %s%s", txid.StringShort(), by)
	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			tracef(env, "AttachTxID: found existing %s%s", txid.StringShort(), by)
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
			vid.SetTxStatus(vertex.Good)
			vid.SetLedgerCoverage(bd.LedgerCoverage)
			env.AddVertexNoLock(vid)
			env.AddBranchNoLock(vid) // <<<< will be reading branch data twice. Not big problem
			tracef(env, "AttachTxID: branch fetched from the state: %s%s", txid.StringShort(), by)
		} else {
			// the corresponding state is not in the multistate DB -> put virtualTx to the utangle_old -> pull it
			// the puller will trigger further solidification
			vid = vertex.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			env.Pull(txid) // always pull new branch. This will spin off sync process on the node
			tracef(env, "AttachTxID: added new branch vertex and pulled %s%s", txid.StringShort(), by)
		}
	})
	return
}

func AttachOutputID(oid ledger.OutputID, env AttachEnvironment, opts ...Option) vertex.WrappedOutput {
	return vertex.WrappedOutput{
		VID:   AttachTxID(oid.TransactionID(), env, opts...),
		Index: oid.Index(),
	}
}

// AttachTransaction attaches new incoming transaction. For sequencer transaction it starts attacher routine
// which manages solidification pull until transaction becomes solid or stopped by the context
func AttachTransaction(tx *transaction.Transaction, env AttachEnvironment, opts ...Option) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
	}
	tracef(env, "AttachTransaction: %s", tx.IDShortString)

	if tx.IsBranchTransaction() {
		env.EvidenceIncomingBranch(tx.ID(), tx.SequencerTransactionData().SequencerID)
	}
	vid = AttachTxID(*tx.ID(), env, OptionDoNotLoadBranch, OptionInvokedBy("addTx"))
	vid.Unwrap(vertex.UnwrapOptions{
		// full vertex will be ignored, virtual tx will be converted into full vertex and attacher started, if necessary
		VirtualTx: func(v *vertex.VirtualTransaction) {
			vid.ConvertVirtualTxToVertexNoLock(vertex.New(tx))

			if !vid.IsSequencerMilestone() {
				// pull non-attached non non-branch input transactions for non-sequencer transactions, which are on the same slot
				// We limit pull to one slot not to fall into endless pull cycle
				tx.PredecessorTransactionIDs().ForEach(func(txid ledger.TransactionID) bool {
					if txid.TimeSlot() == vid.TimeSlot() {
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
			// starts attacher goroutine for sequencer transaction
			ctx := options.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			callback := options.finalizationCallback
			if callback == nil {
				callback = func(_ *vertex.WrappedTx) {}
			}

			runFun := func() {
				status, stats, err := runAttacher(vid, env, ctx)
				vid.SetTxStatus(status)
				vid.SetReason(err)
				env.Log().Info(logFinalStatusString(vid, stats))
				env.PokeAllWith(vid)
				callback(vid)
			}

			const forDebugging = true
			if forDebugging {
				go runFun()
			} else {
				util.RunWrappedRoutine(vid.IDShortString(), runFun, nil, common.ErrDBUnavailable)
			}
		},
	})
	return
}
