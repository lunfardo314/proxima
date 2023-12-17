package attacher

import (
	"context"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
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
		Pull(txid core.TransactionID)
	}

	attacher struct {
		closeMutex          sync.RWMutex
		closed              bool
		inChan              chan *utangle_new.WrappedTx
		ctx                 context.Context
		vid                 *utangle_new.WrappedTx
		baselineStateReader global.IndexedStateReader
		env                 AttachEnvironment
		numMissingTx        uint16
		stemInputIdx        byte
		seqInputIdx         byte
		allInputsAttached   bool
	}
)

const (
	periodicCheckEach       = 500 * time.Millisecond
	maxStateReaderCacheSize = 1000
)

func newAttacher(vid *utangle_new.WrappedTx, env AttachEnvironment, ctx context.Context) *attacher {
	util.Assertf(vid.IsSequencerMilestone(), "sequencer milestone expected")

	var numMissingTx uint16
	vid.Unwrap(utangle_new.UnwrapOptions{Vertex: func(v *utangle_new.Vertex) {
		numMissingTx = uint16(len(v.MissingInputTxIDSet()))
	}})

	ret := &attacher{
		ctx:          ctx,
		vid:          vid,
		env:          env,
		inChan:       make(chan *utangle_new.WrappedTx, 1),
		numMissingTx: numMissingTx,
	}

	return ret
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

func (a *attacher) run() {
	defer a.close()

	a.vid.OnNotify(func(msg *utangle_new.WrappedTx) {
		a.notify(msg)
	})

	for a.processStatus() {
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

}

func (a *attacher) processNotification(vid *utangle_new.WrappedTx) {
	switch vid.GetTxStatus() {
	case utangle_new.TxStatusBad:
		a.vid.SetTxStatus(utangle_new.TxStatusBad)
	case utangle_new.TxStatusGood:
		a.numMissingTx--
	}
}

func (a *attacher) processStatus() bool {
	if !a.vid.IsVertex() {
		// stop once it converted to virtualTx or deleted
		return false
	}

	a.solidifyBaselineIfNeeded()

	if a.vid.GetTxStatus() != utangle_new.TxStatusUndefined {
		a.vid.NotifyFutureCone()
		return false
	}
	if a.numMissingTx > 0 {
		return true
	}
	panic("not implemented")
}

// solidifyBaselineIfNeeded directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) solidifyBaselineIfNeeded() {
	valid := true
	if a.baselineStateReader != nil {
		return
	}

	a.vid.Unwrap(utangle_new.UnwrapOptions{
		Vertex: func(v *utangle_new.Vertex) {
			if v.Tx.IsBranchTransaction() {
				stemInputIdx := v.StemInputIndex()
				if v.Inputs[stemInputIdx] == nil {
					// predecessor stem is absent
					stemInputOid := v.Tx.MustInputAt(stemInputIdx)
					v.Inputs[stemInputIdx] = AttachTxID(stemInputOid.TransactionID(), a.env)
				}
				switch v.Inputs[stemInputIdx].GetTxStatus() {
				case utangle_new.TxStatusGood:
					a.baselineStateReader = v.Inputs[stemInputIdx].BaselineStateReader()
					util.Assertf(a.baselineStateReader != nil, "a.baselineStateReader != nil")
				case utangle_new.TxStatusBad:
					valid = false
				}
				return
			}
			// regular sequencer tx. Go to the direction of the baseline branch
			predOid, predIdx := v.Tx.SequencerChainPredecessor()
			util.Assertf(predOid != nil, "inconsistency: sequencer cannot be at the chain origin")
			if predOid.TimeSlot() == v.Tx.TimeSlot() {
				// predecessor is on the same slot -> continue towards it
				if v.Inputs[predIdx] == nil {
					v.Inputs[predIdx] = AttachTxID(predOid.TransactionID(), a.env)
				}
				if valid = v.Inputs[predIdx].GetTxStatus() != utangle_new.TxStatusBad; valid {
					a.baselineStateReader = v.Inputs[predIdx].BaselineStateReader() // may be nil
				}
				return
			}
			// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
			util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
			if v.Endorsements[0] == nil {
				v.Endorsements[0] = AttachTxID(v.Tx.EndorsementAt(0), a.env)
			}
			if valid = v.Endorsements[0].GetTxStatus() != utangle_new.TxStatusBad; valid {
				a.baselineStateReader = v.Endorsements[0].BaselineStateReader() // may be nil
			}
		},
		VirtualTx: a.vid.PanicShouldNotBeVirtualTx,
		Deleted:   a.vid.PanicAccessDeleted,
	})
	if !valid {
		a.vid.SetTxStatus(utangle_new.TxStatusBad)
		return
	}
	if !a.vid.IsBranchTransaction() {
		a.vid.SetBaselineStateReader(a.baselineStateReader)
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
//			v.Inputs[stemInputIdx] = AttachTxID(stemInputTxID, env)
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
//	v.Inputs[seqInputIdx] = AttachTxID(seqInputTxID, env)
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
//			v.Inputs[i] = AttachTxID(oid.TransactionID(), env)
//		}
//		success = v.Inputs[i].GetTxStatus() != utangle_new.TxStatusBad
//		return success
//	})
//	if !success {
//		return false, false
//	}
//	v.Tx.ForEachEndorsement(func(idx byte, txid *core.TransactionID) bool {
//		if v.Endorsements[idx] == nil {
//			v.Endorsements[idx] = AttachTxID(*txid, env)
//		}
//		success = v.Endorsements[idx].GetTxStatus() != utangle_new.TxStatusBad
//		return success
//	})
//	return success, success
//}
