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
		solidPastCone       set.Set[*vertex.WrappedTx]
		pendingPastCone     set.Set[*vertex.WrappedTx]
	}
)

const (
	periodicCheckEach = 500 * time.Millisecond
)

func newAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) *attacher {
	ret := &attacher{
		ctx:             ctx,
		vid:             vid,
		env:             env,
		inChan:          make(chan *vertex.WrappedTx, 1),
		solidPastCone:   set.New[*vertex.WrappedTx](),
		pendingPastCone: set.New[*vertex.WrappedTx](),
	}
	ret.vid.OnNotify(func(msg *vertex.WrappedTx) {
		ret.notify(msg)
	})
	return ret
}

func runAttacher(vid *vertex.WrappedTx, env AttachEnvironment, ctx context.Context) {
	a := newAttacher(vid, env, ctx)
	// first solidify baseline state
	a.runWhileNotFinalAnd(func(v *vertex.Vertex) bool {
		if a.baselineBranch == nil {
			a._solidifyBaseline(v)
		}
		return a.baselineBranch == nil
	})
	// then continue with the rest
	a.runWhileNotFinalAnd(func(v *vertex.Vertex) bool {
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

func (a *attacher) runWhileNotFinalAnd(processVertex func(v *vertex.Vertex) bool) {
	for !a.isFinalStatus() {
		invalid := true
		a.vid.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				invalid = !processVertex(v)
			},
		})
		if invalid {
			a.vid.SetTxStatus(vertex.TxStatusBad)
			return
		}
		if a.isFinalStatus() {
			return
		}

		select {
		case <-a.ctx.Done():
			return

		case downstreamVID := <-a.inChan:
			if downstreamVID == nil {
				return
			}
			a.processNotification(downstreamVID)

		case <-time.After(periodicCheckEach):
		}
	}
	return
}

func (a *attacher) isFinalStatus() bool {
	return !a.vid.IsVertex() || a.vid.GetTxStatus() != vertex.TxStatusUndefined
}

// _solidifyBaseline directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) _solidifyBaseline(v *vertex.Vertex) {
	if v.Tx.IsBranchTransaction() {
		a._solidifyStem(v)
	} else {
		a._solidifySequencerBaseline(v)
	}
	if a.baselineBranch != nil {
		a.baselineStateReader = multistate.MakeSugared(a.env.GetBaselineStateReader(a.baselineBranch))
	}
}

func (a *attacher) _solidifyStem(v *vertex.Vertex) {
	stemInputIdx := v.StemInputIndex()
	if v.Inputs[stemInputIdx] == nil {
		// predecessor stem is pending
		stemInputOid := v.Tx.MustInputAt(stemInputIdx)
		v.Inputs[stemInputIdx] = AttachTxID(stemInputOid.TransactionID(), a.env, false)
		util.Assertf(v.Inputs[stemInputIdx] != nil, "v.Inputs[stemInputIdx] != nil")
	}
	switch v.Inputs[stemInputIdx].GetTxStatus() {
	case vertex.TxStatusGood:
		a.baselineBranch = v.Inputs[stemInputIdx].BaselineBranch()
		util.Assertf(a.baselineBranch != nil, "a.baselineBranch != nil")
	case vertex.TxStatusBad:
		a.vid.SetTxStatus(vertex.TxStatusBad)
	}
}

func (a *attacher) _solidifySequencerBaseline(v *vertex.Vertex) {
	// regular sequencer tx. Go to the direction of the baseline branch
	predOid, predIdx := v.Tx.SequencerChainPredecessor()
	util.Assertf(predOid != nil, "inconsistency: sequencer milestone cannot be a chain origin")
	var inputTx *vertex.WrappedTx

	if predOid.TimeSlot() == v.Tx.TimeSlot() {
		// predecessor is on the same slot -> continue towards it
		if v.Inputs[predIdx] == nil {
			v.Inputs[predIdx] = AttachTxID(predOid.TransactionID(), a.env, true)
			util.Assertf(v.Inputs[predIdx] != nil, "v.Inputs[predIdx] != nil")
		}
		inputTx = v.Inputs[predIdx]
	} else {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		if v.Endorsements[0] == nil {
			v.Endorsements[0] = AttachTxID(v.Tx.EndorsementAt(0), a.env, true)
		}
		inputTx = v.Endorsements[0]
	}
	if inputTx.GetTxStatus() == vertex.TxStatusBad {
		a.vid.SetTxStatus(vertex.TxStatusBad)
	} else {
		a.baselineBranch = inputTx.BaselineBranch() // may be nil
	}
}

func (a *attacher) processNotification(vid *vertex.WrappedTx) {
	vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			if vid.GetTxStatusNoLock() == vertex.TxStatusBad {
				a.vid.SetTxStatus(vertex.TxStatusBad)
				return
			}

		},
		VirtualTx: nil,
		Deleted:   vid.PanicAccessDeleted,
	})
	if vid.GetTxStatus() == vertex.TxStatusBad {
		a.vid.SetTxStatus(vertex.TxStatusBad)
		return
	}

}

// TODO

func (a *attacher) runPastCone(v *vertex.Vertex) bool {
	if !a._solidifyInputs(v) {
		return false
	}
	a.runAllPending()
	panic("not implemented")
}

func (a *attacher) _solidifyInputs(v *vertex.Vertex) bool {
	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")

	invalid := false
	v.ForEachInputDependency(func(i byte, vidInput *vertex.WrappedTx) bool {
		if vidInput == nil {
			vidInput = AttachInput(a.vid, i, a.env, &a.baselineStateReader, true)
			if vidInput == nil {
				invalid = true
				return false
			}
			v.Inputs[i] = vidInput
			a.pendingPastCone.Insert(vidInput)
		}
		if vidInput.GetTxStatus() == vertex.TxStatusBad {
			invalid = true
			return false
		}
		return true
	})
	if invalid {
		return false
	}
	v.ForEachEndorsement(func(i byte, vidEndorsed *vertex.WrappedTx) bool {
		if vidEndorsed == nil {
			vidEndorsed = AttachTxID(v.Tx.EndorsementAt(i), a.env, true)
			util.Assertf(vidEndorsed != nil, "v.Endorsements[i] != nil")
			a.pendingPastCone.Insert(vidEndorsed)
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

func (a *attacher) runAllPending() bool {
	descPending := util.KeysSorted(a.pendingPastCone, func(k1, k2 *vertex.WrappedTx) bool {
		return !core.LessTxID(*k1.ID(), *k2.ID())
	})
	for _, pendingVID := range descPending {
		util.Assertf(!a.solidPastCone.Contains(pendingVID), "!a.solidPastCone.Contains(pendingVID)")
		if a.visitPending(pendingVID) {
			delete(a.pendingPastCone, pendingVID)
			a.solidPastCone.Insert(pendingVID)
		}
	}
	panic("not implemented")
}

func (a *attacher) visitPending(vid *vertex.WrappedTx) bool {
	panic("not implemented")
}
