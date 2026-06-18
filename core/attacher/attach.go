package attacher

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

// numAttachers is the authoritative count of running sequencer attacher goroutines.
// Incremented before the goroutine starts (synchronously in AttachTransaction),
// decremented when the goroutine finishes.
var numAttachers atomic.Int32

// NumAttachers returns the current number of running sequencer attacher goroutines.
func NumAttachers() int {
	return int(numAttachers.Load())
}


// AttachTxID ensures the txid is on the MemDAG
// It load existing branches but does not pullFromPeers anything
func AttachTxID(txid base.TransactionID, env Environment, opts ...AttachTxOption) (vid *vertex.WrappedTx) {
	options := &_attacherOptions{}
	for _, opt := range opts {
		opt(options)
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
			// KnowsCommittedTransaction returned false. This can happen when the txID key
			// has been deleted from the trie (TTL expiry) AND all outputs are consumed.
			// For such ancient branches, treat as GOOD — they were committed before the snapshot.
			snapSlot := snapID.Slot()
			txSlot := txid.Slot()
			if txSlot < snapSlot && snapSlot-txSlot > ledger.L(snapSlot).TxIDStateTTLSlots {
				vid.SetTxStatusGoodNoLock(nil, 0)
			} else {
				// it is not in the snapshot state -> mark it BAD branch
				err := fmt.Errorf("baseline branch state %s is before snapshot slot %d and is not available -> can't solidify baseline",
					txid.String(), snapID.Slot())
				vid.SetTxStatusBadNoLock(err)
			}
		}
	})
	return
}

// childAttachmentDepth returns the attachment depth to assign to a dependency
// reached from a parent at parentDepth. Depth counts BRANCHES on the backward
// walk — lineage distance, roughly "how many slots behind" — per
// claude/sync_semantics.md §2.1: it increments only when the dependency is a
// branch transaction and stays the same across the non-branch sequencer
// transactions within a slot. (Previously it incremented per vertex, which
// false-capped tip-adjacent past cones by breadth — the 2026-06-18 leak.)
func childAttachmentDepth(parentDepth int, childID base.TransactionID) int {
	if childID.IsBranchTransaction() {
		return parentDepth + 1
	}
	return parentDepth
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

	txid := tx.ID()
	vid = AttachTxID(txid, env, WithInvokedBy("addTx"))

	// Check if vid is a DetachedVertex (read lock only — no contention on the common path).
	// If so, reattach in-place: the vertex was GC'd but its *transaction.Transaction
	// is immutable and still valid. Reset flags and convert back to fresh Vertex.
	isDetached := false
	vid.RUnwrap(vertex.UnwrapOptions{
		DetachedVertex: func(_ *vertex.DetachedVertex) {
			isDetached = true
		},
	})
	if isDetached {
		reattached := false
		vid.Unwrap(vertex.UnwrapOptions{
			DetachedVertex: func(v *vertex.DetachedVertex) {
				if vid.FlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted) && !vid.FlagsUpNoLock(vertex.FlagVertexTxAttachmentFinished) {
					return // reattachment already in progress (started but not yet finished)
				}
				// Either never attached, or attachment completed then GC'd — allow reattachment
				env.Log().Infof("REATTACH START %s seq=%v", txid.StringShort(), txid.IsSequencerTransaction())
				vid.ReattachVertexNoLock(v.Transaction)

				if vid.IsSequencerTransaction() {
					numAttachers.Add(1)
					go func() {
						defer numAttachers.Add(-1)
						env.IncCounter("att")
						defer env.DecCounter("att")

						env.MarkWorkProcessStarted(vid.IDShortString() + "_reattach")
						started := time.Now()
						cost := runMilestoneAttacher(vid, nil, nil, env, nil)
						env.MarkWorkProcessStopped(vid.IDShortString() + "_reattach")
						env.AttachmentFinished(started, cost)
					}()
				}
				reattached = true
			},
		})
		if reattached {
			env.PokeAllWith(vid)
			return
		}
	}

	if env.Branches().TransactionIsInSnapshotState(txid) {
		// Transaction is in the snapshot state — it was committed before the snapshot.
		// Convert to full vertex and mark GOOD so that dependent attachers can proceed,
		// but don't start an attacher (no need to validate already-committed transactions).
		vid.UnwrapVirtualTx(func(v *vertex.VirtualTransaction) {
			if vid.FlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted) {
				return
			}
			vid.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted)
			vid.ConvertVirtualTxToVertexNoLock(vertex.NewVertex(tx))
			if vid.GetTxStatusNoLock() != vertex.Good {
				vid.SetTxStatusGoodNoLock(nil, 0)
			}
		})
		return vid
	}

	// Track whether this attachment is new (not already started) so we can post events
	// outside the vertex lock to avoid backpressure blocking the lock holder.
	newlyAttached := false

	vid.UnwrapVirtualTx(func(_ *vertex.VirtualTransaction) {
		if vid.FlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted) {
			// case with already attached transaction
			if options.attachmentCallback != nil {
				go func() {
					options.attachmentCallback(vid, vid.GetErrorNoLock())
				}()
			}
			return
		}

		// mark the vertex to prevent repetitive attachment
		vid.SetFlagsUpNoLock(vertex.FlagVertexTxAttachmentStarted)
		env.LogTx(time.Now(), fmt.Sprintf("ATTACH START seq=%v", txid.IsSequencerTransaction()), txid)

		// virtual tx is converted into full vertex with the full transaction
		vid.ConvertVirtualTxToVertexNoLock(vertex.NewVertex(tx))

		if vid.IsSequencerTransaction() {
			// for sequencer milestones start attacher
			metadata := options.metadata
			numAttachers.Add(1) // increment synchronously, before goroutine starts
			// start attacher routine
			go func() {
				defer numAttachers.Add(-1)
				env.IncCounter("att")
				defer env.DecCounter("att")

				env.MarkWorkProcessStarted(vid.IDShortString())
				started := time.Now()
				cost := runMilestoneAttacher(vid, metadata, options.attachmentCallback, env, options.ctx)
				env.MarkWorkProcessStopped(vid.IDShortString())

				env.AttachmentFinished(started, cost)
			}()
		}
		if !vid.IsSequencerTransaction() {
			env.IncCounter("nonseq")
		}
		// significantly speeds up non-sequencer transactions
		if !vid.IsSequencerTransaction() || vid.IsBranchTransaction() {
			env.PokeAllWith(vid)
		}

		newlyAttached = true
	})

	// Post events outside the vertex lock to prevent backpressure from the event queue
	// blocking the lock holder and causing cascading deadlocks under high TPS.
	if newlyAttached {
		env.PostEventNewTransaction(vid)

		if !vid.IsSequencerTransaction() {
			env.PostEventNewVertex(tx, "")
		}
	}
	return
}

// AttachTransactionFromBytes used for testing
func AttachTransactionFromBytes(txBytes []byte, env Environment, opts ...AttachTxOption) (*vertex.WrappedTx, error) {
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		return nil, err
	}
	return AttachTransaction(tx, env, opts...), nil
}

// InvalidateTxID marks existing vertex as BAD or creates new BAD
func InvalidateTxID(txid base.TransactionID, env Environment, reason error) {
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
