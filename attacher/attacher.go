package attacher

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle_new"
	"github.com/lunfardo314/proxima/util"
	"go.uber.org/zap"
)

type (
	AttachEnvironment interface {
		Log() *zap.SugaredLogger
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *core.TransactionID) *utangle_new.WrappedTx
		AddVertexNoLock(vid *utangle_new.WrappedTx)
		GetWrappedOutput(oid *core.OutputID) (utangle_new.WrappedOutput, bool)
		GetVertex(txid *core.TransactionID) *utangle_new.WrappedTx
		StateStore() global.StateStore
		GetBaselineStateReader(branch *utangle_new.WrappedTx) global.IndexedStateReader
		AddBranchNoLock(branch *utangle_new.WrappedTx, branchData *multistate.BranchData)
		Pull(txid core.TransactionID)
	}

	attacher struct {
		closeMutex          sync.RWMutex
		closed              bool
		inChan              chan *utangle_new.WrappedTx
		ctx                 context.Context
		vid                 *utangle_new.WrappedTx
		baselineBranch      *utangle_new.WrappedTx
		baselineStateReader multistate.SugaredStateReader
		env                 AttachEnvironment
	}
)

const (
	periodicCheckEach = 500 * time.Millisecond
)

func newAttacher(vid *utangle_new.WrappedTx, env AttachEnvironment, ctx context.Context) *attacher {
	ret := &attacher{
		ctx:    ctx,
		vid:    vid,
		env:    env,
		inChan: make(chan *utangle_new.WrappedTx, 1),
	}
	ret.vid.OnNotify(func(msg *utangle_new.WrappedTx) {
		ret.notify(msg)
	})
	return ret
}

func runAttacher(vid *utangle_new.WrappedTx, env AttachEnvironment, ctx context.Context) {
	a := newAttacher(vid, env, ctx)
	// first solidify baseline state
	a.runWhileNotFinalAnd(func(v *utangle_new.Vertex) bool {
		if a.baselineBranch == nil {
			a._solidifyBaseline(v)
		}
		return a.baselineBranch == nil
	})
	if a.isFinalStatus() {
		return
	}
	// then continue with the rest
	a.runWhileNotFinalAnd(func(v *utangle_new.Vertex) bool {
		return a.runInputs(v)
	})
}

func (a *attacher) close() {
	a.closeMutex.Lock()
	defer a.closeMutex.Unlock()

	a.closed = true
	a.vid.OnNotify(nil)
	close(a.inChan)
}

func (a *attacher) notify(msg *utangle_new.WrappedTx) {
	a.closeMutex.RLock()
	defer a.closeMutex.RUnlock()

	if !a.closed {
		a.inChan <- msg
	}
}

func (a *attacher) runWhileNotFinalAnd(processVertex func(v *utangle_new.Vertex) bool) {
	for !a.isFinalStatus() {
		exit := true
		a.vid.Unwrap(utangle_new.UnwrapOptions{
			Vertex: func(v *utangle_new.Vertex) {
				exit = !processVertex(v)
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
			a.processNotification(downstreamVID)

		case <-time.After(periodicCheckEach):
		}
	}
	return
}

func (a *attacher) isFinalStatus() bool {
	return !a.vid.IsVertex() || a.vid.GetTxStatus() != utangle_new.TxStatusUndefined
}

// _solidifyBaseline directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) _solidifyBaseline(v *utangle_new.Vertex) {
	if v.Tx.IsBranchTransaction() {
		a._solidifyStem(v)
	} else {
		a._solidifySequencerBaseline(v)
	}
	if a.baselineBranch != nil {
		a.baselineStateReader = multistate.MakeSugared(a.env.GetBaselineStateReader(a.baselineBranch))
	}
}

func (a *attacher) _solidifyStem(v *utangle_new.Vertex) {
	stemInputIdx := v.StemInputIndex()
	if v.Inputs[stemInputIdx] == nil {
		// predecessor stem is pending
		stemInputOid := v.Tx.MustInputAt(stemInputIdx)
		v.Inputs[stemInputIdx] = AttachTxID(stemInputOid.TransactionID(), a.env)
		util.Assertf(v.Inputs[stemInputIdx] != nil, "v.Inputs[stemInputIdx] != nil")
	}
	switch v.Inputs[stemInputIdx].GetTxStatus() {
	case utangle_new.TxStatusGood:
		a.baselineBranch = v.Inputs[stemInputIdx].BaselineBranch()
		util.Assertf(a.baselineBranch != nil, "a.baselineBranch != nil")
	case utangle_new.TxStatusBad:
		a.vid.SetTxStatus(utangle_new.TxStatusBad)
	}
}

func (a *attacher) _solidifySequencerBaseline(v *utangle_new.Vertex) {
	// regular sequencer tx. Go to the direction of the baseline branch
	predOid, predIdx := v.Tx.SequencerChainPredecessor()
	util.Assertf(predOid != nil, "inconsistency: sequencer cannot be at the chain origin")
	var inputTx *utangle_new.WrappedTx

	if predOid.TimeSlot() == v.Tx.TimeSlot() {
		// predecessor is on the same slot -> continue towards it
		if v.Inputs[predIdx] == nil {
			v.Inputs[predIdx] = AttachTxID(predOid.TransactionID(), a.env)
			util.Assertf(v.Inputs[predIdx] != nil, "v.Inputs[predIdx] != nil")
		}
		inputTx = v.Inputs[predIdx]
	} else {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		if v.Endorsements[0] == nil {
			v.Endorsements[0] = AttachTxID(v.Tx.EndorsementAt(0), a.env)
		}
		inputTx = v.Endorsements[0]
	}
	if inputTx.GetTxStatus() == utangle_new.TxStatusBad {
		a.vid.SetTxStatus(utangle_new.TxStatusBad)
	} else {
		a.baselineBranch = inputTx.BaselineBranch() // may be nil
	}
}

func (a *attacher) processNotification(vid *utangle_new.WrappedTx) {
	switch vid.GetTxStatus() {
	case utangle_new.TxStatusBad:
		a.vid.SetTxStatus(utangle_new.TxStatusBad)
	case utangle_new.TxStatusGood:
	}
}

// TODO

func (a *attacher) runInputs(v *utangle_new.Vertex) bool {
	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")
	bad := false
	v.ForEachInputDependency(func(i byte, vidInput *utangle_new.WrappedTx) bool {
		if vidInput == nil {
			v.Inputs[i] = AttachInput(a.vid, i, a.env)
		}
		if v.Inputs[i] == nil || v.Inputs[i].GetTxStatus() == utangle_new.TxStatusBad {
			bad = true
			return false
		}
		return true
	})
	if bad {
		a.vid.SetTxStatus(utangle_new.TxStatusBad)
		return false
	}
	v.ForEachEndorsement(func(i byte, vidEndorsed *utangle_new.WrappedTx) bool {
		if vidEndorsed == nil {
			v.Endorsements[i] = AttachTxID(v.Tx.EndorsementAt(i), a.env)
		}
		if v.Endorsements[i].GetTxStatus() != utangle_new.TxStatusBad {
			bad = true
			return false
		}
		return true
	})
	if bad {
		a.vid.SetTxStatus(utangle_new.TxStatusBad)
	}
}

//
//// attachInputTransactionsIfNeeded does not check correctness of output indices, only transaction status
//func attachInputTransactionsIfNeeded(v *utangle_new.Vertex, env AttachEnvironment) (bool, bool) {
//	var stemInputTxID, seqInputTxID core.TransactionID
//
//	if v.Tx.IsBranchTransaction() {
//		stemInputIdx := v.StemInputIndex()
//		if v.Inputs[stemInputIdx] == nil {
//			stemInputOid := v.Tx.MustInputAt(stemInputIdx)
//			stemInputTxID = stemInputOid.TransactionID()
//			v.Inputs[stemInputIdx] = _attachTxID(stemInputTxID, env)
//		}
//		switch v.Inputs[stemInputIdx].GetTxStatus() {
//		case utangle_new.TxStatusBad:
//			return false, false
//		case utangle_new.TxStatusUndefined:
//			return true, false
//		}
//	}
//	// stem is good
//	seqInputIdx := v.SequencerInputIndex()
//	seqInputOid := v.Tx.MustInputAt(seqInputIdx)
//	seqInputTxID = seqInputOid.TransactionID()
//	v.Inputs[seqInputIdx] = _attachTxID(seqInputTxID, env)
//	switch v.Inputs[seqInputIdx].GetTxStatus() {
//	case utangle_new.TxStatusBad:
//		return false, false
//	case utangle_new.TxStatusUndefined:
//		return true, false
//	}
//	// stem and seq inputs are ok. We can pull the rest
//	missing := v.MissingInputTxIDSet().Remove(seqInputTxID)
//	if v.Tx.IsBranchTransaction() {
//		missing.Remove(stemInputTxID)
//	}
//	success := true
//	v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
//		if v.Inputs[i] == nil {
//			v.Inputs[i] = _attachTxID(oid.TransactionID(), env)
//		}
//		success = v.Inputs[i].GetTxStatus() != utangle_new.TxStatusBad
//		return success
//	})
//	if !success {
//		return false, false
//	}
//	v.Tx.ForEachEndorsement(func(idx byte, txid *core.TransactionID) bool {
//		if v.Endorsements[idx] == nil {
//			v.Endorsements[idx] = _attachTxID(*txid, env)
//		}
//		success = v.Endorsements[idx].GetTxStatus() != utangle_new.TxStatusBad
//		return success
//	})
//	return success, success
//}
