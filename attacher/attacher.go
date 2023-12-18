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
		baselineStateReader global.IndexedStateReader
		env                 AttachEnvironment
	}
)

const (
	periodicCheckEach       = 500 * time.Millisecond
	maxStateReaderCacheSize = 1000
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
	a.runWhileNotFinal(func(v *utangle_new.Vertex) bool {
		a.solidifyBaselineIfNeeded(v)
		return a.baselineBranch == nil
	})
	if a.isFinalStatus() {
		return
	}
	a.baselineStateReader = env.GetBaselineStateReader(a.baselineBranch)
	// then continue with the rest
	a.runWhileNotFinal(func(v *utangle_new.Vertex) bool {
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

func (a *attacher) runWhileNotFinal(processVertex func(v *utangle_new.Vertex) bool) {
	var exit bool
	for !a.isFinalStatus() {
		a.vid.Unwrap(utangle_new.UnwrapOptions{
			Vertex: func(v *utangle_new.Vertex) {
				exit = !processVertex(v)
			},
			VirtualTx: a.vid.PanicShouldNotBeVirtualTx,
			Deleted:   a.vid.PanicAccessDeleted,
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

// solidifyBaselineIfNeeded directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) solidifyBaselineIfNeeded(v *utangle_new.Vertex) {
	if a.baselineBranch != nil {
		return
	}
	if v.Tx.IsBranchTransaction() {
		a._solidifyStem(v)
	} else {
		a._solidifySequencerBaseline(v)
	}
}

func (a *attacher) _solidifyStem(v *utangle_new.Vertex) {
	stemInputIdx := v.StemInputIndex()
	if v.Inputs[stemInputIdx] == nil {
		// predecessor stem is pending
		v.Inputs[stemInputIdx] = AttachInput(a.vid, stemInputIdx, a.env)
		if v.Inputs[stemInputIdx] == nil {
			a.vid.SetTxStatus(utangle_new.TxStatusBad)
			return
		}
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
	if predOid.TimeSlot() == v.Tx.TimeSlot() {
		// predecessor is on the same slot -> continue towards it
		if v.Inputs[predIdx] == nil {
			v.Inputs[predIdx] = AttachInput(a.vid, predIdx, a.env)
			if v.Inputs[predIdx] == nil {
				a.vid.SetTxStatus(utangle_new.TxStatusBad)
				return
			}
		}
		if v.Inputs[predIdx].GetTxStatus() != utangle_new.TxStatusBad {
			a.baselineBranch = v.Inputs[predIdx].BaselineBranch() // may be nil
		}
		return
	}
	// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
	util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
	if v.Endorsements[0] == nil {
		v.Endorsements[0] = AttachTxID(v.Tx.EndorsementAt(0), a.env)
	}
	if v.Endorsements[0].GetTxStatus() != utangle_new.TxStatusBad {
		a.baselineBranch = v.Endorsements[0].BaselineBranch() // may be nil
		a.vid.SetBaselineBranch(a.baselineBranch)
	} else {
		a.vid.SetTxStatus(utangle_new.TxStatusBad)
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
