package attacher

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle_new/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"go.uber.org/zap"
)

type (
	AttachEnvironment interface {
		Log() *zap.SugaredLogger
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *core.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		GetWrappedOutput(oid *core.OutputID) (vertex.WrappedOutput, bool)
		GetVertex(txid *core.TransactionID) *vertex.WrappedTx
		StateStore() global.StateStore
		GetBaselineStateReader(branch *vertex.WrappedTx) global.IndexedStateReader
		AddBranchNoLock(branch *vertex.WrappedTx, branchData *multistate.BranchData)
		Pull(txid core.TransactionID)
	}

	attacher struct {
		closeMutex          sync.RWMutex
		closed              bool
		inChan              chan *vertex.WrappedTx
		ctx                 context.Context
		vid                 *vertex.WrappedTx
		baselineBranch      *vertex.WrappedTx
		baselineStateReader multistate.SugaredStateReader
		env                 AttachEnvironment
		visited             set.Set[*vertex.WrappedTx]
		rooted              set.Set[*vertex.WrappedTx] // those in the past cone known in the baseline
	}
)

const (
	periodicCheckEach = 500 * time.Millisecond
)

func newAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) *attacher {
	ret := &attacher{
		ctx:     ctx,
		vid:     vid,
		env:     env,
		inChan:  make(chan *vertex.WrappedTx, 1),
		visited: set.New[*vertex.WrappedTx](),
		rooted:  set.New[*vertex.WrappedTx](),
	}
	ret.vid.OnNotify(func(msg *vertex.WrappedTx) {
		ret.notify(msg)
	})
	return ret
}

func runAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) {
	a := newAttacher(vid, env, ctx)
	defer a.close()

	// first solidify baseline state
	status := a.lazyRepeatUntil(func(v *vertex.Vertex) (exit bool, status vertex.TxStatus) {
		if a.baselineBranch == nil {
			status = a._solidifyBaseline(v)
		}
		return a.baselineBranch != nil, status
	})
	a.vid.SetTxStatus(status)
	if a.isFinalStatus() {
		return
	}
	util.Assertf(a.baselineBranch != nil, "a.baselineBranch != nil")
	// baseline is solid, i.e. we know the baseline state the transactions must be solidified upon
	a.baselineStateReader = multistate.MakeSugared(a.env.GetBaselineStateReader(a.baselineBranch))

	// then continue with the rest
	a.lazyRepeatUntil(func(v *vertex.Vertex) (exit bool, status vertex.TxStatus) {
		return a.runPastCone(v)
	})
}

func (a *attacher) close() {
	a.closeMutex.Lock()
	defer a.closeMutex.Unlock()

	a.closed = true
	a.vid.OnNotify(nil)
	close(a.inChan)
}

func (a *attacher) notify(msg *vertex.WrappedTx) {
	a.closeMutex.RLock()
	defer a.closeMutex.RUnlock()

	if !a.closed {
		a.inChan <- msg
	}
}

func (a *attacher) lazyRepeatUntil(processVertex func(v *vertex.Vertex) (exit bool, status vertex.TxStatus)) (status vertex.TxStatus) {
	var exit bool

	for {
		exit = true
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				exit, status = processVertex(v)
			},
		})
		if exit || a.isFinalStatus() {
			return
		}

		select {
		case <-a.ctx.Done():
			return

		case downstreamVID := <-a.inChan:
			if downstreamVID == nil {
				return
			}
			a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
				exit, status = a.processNotification(v, downstreamVID)
			}})
			if exit || a.isFinalStatus() {
				return
			}

		case <-time.After(periodicCheckEach):
		}
	}
}

func (a *attacher) isFinalStatus() bool {
	return !a.vid.IsVertex() || a.vid.GetTxStatus() != vertex.TxStatusUndefined
}

// _solidifyBaseline directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) _solidifyBaseline(v *vertex.Vertex) (status vertex.TxStatus) {
	if v.Tx.IsBranchTransaction() {
		status = _solidifyStem(v, a.env)
	} else {
		status = _solidifySequencerBaseline(v, a.env)
	}
	a.baselineBranch = v.BaselineBranch
	return
}

func _solidifyStem(v *vertex.Vertex, env AttachEnvironment) (status vertex.TxStatus) {
	status = vertex.TxStatusUndefined
	stemInputIdx := v.StemInputIndex()
	if v.Inputs[stemInputIdx] == nil {
		// predecessor stem is pending
		stemInputOid := v.Tx.MustInputAt(stemInputIdx)
		v.Inputs[stemInputIdx] = attachTxID(stemInputOid.TransactionID(), env, false)
		util.Assertf(v.Inputs[stemInputIdx] != nil, "v.Inputs[stemInputIdx] != nil")
	}
	switch v.Inputs[stemInputIdx].GetTxStatus() {
	case vertex.TxStatusGood:
		v.BaselineBranch = v.Inputs[stemInputIdx].BaselineBranch()
		util.Assertf(v.BaselineBranch != nil, "a.baselineBranch != nil")
	case vertex.TxStatusBad:
		status = vertex.TxStatusBad
	}
	return
}

func _solidifySequencerBaseline(v *vertex.Vertex, env AttachEnvironment) (status vertex.TxStatus) {
	status = vertex.TxStatusUndefined
	// regular sequencer tx. Go to the direction of the baseline branch
	predOid, predIdx := v.Tx.SequencerChainPredecessor()
	util.Assertf(predOid != nil, "inconsistency: sequencer milestone cannot be a chain origin")
	var inputTx *vertex.WrappedTx

	if predOid.TimeSlot() == v.Tx.TimeSlot() {
		// predecessor is on the same slot -> continue towards it
		if v.Inputs[predIdx] == nil {
			v.Inputs[predIdx] = attachTxID(predOid.TransactionID(), env, true)
			util.Assertf(v.Inputs[predIdx] != nil, "v.Inputs[predIdx] != nil")
		}
		inputTx = v.Inputs[predIdx]
	} else {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		if v.Endorsements[0] == nil {
			v.Endorsements[0] = attachTxID(v.Tx.EndorsementAt(0), env, true)
		}
		inputTx = v.Endorsements[0]
	}
	if inputTx.GetTxStatus() == vertex.TxStatusBad {
		status = vertex.TxStatusBad
	} else {
		v.BaselineBranch = inputTx.BaselineBranch() // may be nil
	}
	return
}

func (a *attacher) processNotification(v *vertex.Vertex, vid *vertex.WrappedTx) (exit bool, status vertex.TxStatus) {
	panic("not implemented")
}

// TODO

func (a *attacher) runPastCone(v *vertex.Vertex) (exit bool, status vertex.TxStatus) {
	//if !a._attachInputs(v) {
	//	return true, true
	//}
	a.runAllPendingNoSequencer()
	panic("not implemented")
}

func (a *attacher) attachInput(wOut vertex.WrappedOutput, consumer *vertex.WrappedTx) (status vertex.TxStatus) {
	rooted := false
	if !a.rooted.Contains(wOut.VID) {
		if a.baselineStateReader.KnowsCommittedTransaction(wOut.VID.ID()) {
			a.rooted.Insert(wOut.VID)
			rooted = true
		}
	} else {
		rooted = true
	}
	if rooted {
		oid := wOut.DecodeID()
		out := a.baselineStateReader.GetOutput(oid)
		vidRet := attachInput(consumer, oid, a.env, out)
		util.Assertf(vidRet == wOut.VID, "vidRet==wOut.VID")
		status = wOut.VID.GetTxStatus()
		if status != vertex.TxStatusUndefined {
			return
		}
		wOut.VID.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {

			},
			VirtualTx: func(v *vertex.VirtualTransaction) {

			},
			Deleted: wOut.VID.PanicAccessDeleted,
		})
		return
	}

}

func (a *attacher) _attachInputs(v *vertex.Vertex) (status vertex.TxStatus) {
	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")

	status = vertex.TxStatusUndefined
	v.ForEachInputDependency(func(i byte, vidInput *vertex.WrappedTx) bool {
		if vidInput != nil {
			if v.GoodInputs[i] {
				return true
			}
			switch vidInput.GetTxStatus() {
			case vertex.TxStatusGood:
				v.GoodInputs[i] = true
			case vertex.TxStatusBad:
				status = vertex.TxStatusBad
				return false // >>>>>>> invalidate the whole tx
			case vertex.TxStatusUndefined:
				if !vidInput.IsSequencerMilestone() {
					if len(a.visited) == 0 {
						// pending list cleared -> all good
						v.GoodInputs[i] = true
					}
				}
			}
			return true
		}
		//
		inOid := v.Tx.MustInputAt(i)
		inTxID := inOid.TransactionID()
		var out *core.Output
		if a.baselineStateReader.KnowsCommittedTransaction(&inTxID) {
			if out = a.baselineStateReader.GetOutput(&inOid); out == nil {
				// if transaction is known but output is not there -> already spent (double spend) -> retStatus input
				status = vertex.TxStatusBad
				return false
			}
		}
		vidInput = attachInput(a.vid, &inOid, a.env, out)
		if vidInput == nil {
			status = vertex.TxStatusBad
			return false
		}
		v.Inputs[i] = vidInput
		if !inTxID.IsSequencerMilestone() && out == nil {
			a.visited.Insert(vidInput)
		}

		if vidInput.GetTxStatus() == vertex.TxStatusBad {
			status = vertex.TxStatusBad
			return false
		}
		return true
	})
	return retStatus
}

func (a *attacher) _attachEndorsements(v *vertex.Vertex) (invalid bool) {
	v.ForEachEndorsement(func(i byte, vidEndorsed *vertex.WrappedTx) bool {
		if vidEndorsed == nil {
			vidEndorsed = attachTxID(v.Tx.EndorsementAt(i), a.env, true)
			util.Assertf(vidEndorsed != nil, "v.Endorsements[i] != nil")
			a.visited.Insert(vidEndorsed)
			v.Endorsements[i] = vidEndorsed
		}
		if vidEndorsed.GetTxStatus() == vertex.TxStatusBad {
			invalid = true
			return false
		}
		return true
	})
	return !invalid

}

func (a *attacher) runAllPendingNoSequencer() bool {
	descPending := util.KeysSorted(a.visited, func(k1, k2 *vertex.WrappedTx) bool {
		return !core.LessTxID(*k1.ID(), *k2.ID())
	})
	for _, pendingVID := range descPending {
	}
}

func (a *attacher) attachPendingNonSequencerOutput() (invalid bool) {

}

func (a *attacher) visitPending(vid *vertex.WrappedTx) bool {
	panic("not implemented")
}
