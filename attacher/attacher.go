package attacher

import (
	"context"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
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
		Pull(txid *core.TransactionID)
	}

	attacher struct {
		inChan            chan any
		vid               *utangle_new.WrappedTx
		env               AttachEnvironment
		numMissingTx      uint16
		stemInputIdx      byte
		seqInputIdx       byte
		allInputsAttached bool
	}
)

const periodicCheckEach = 500 * time.Millisecond

// AttachTxID ensures the txid is on the utangle and it is pulled.
// If it is not on the utange, creates TxID vertex and pulls. Otherwise, it does not touch it. It returns VID of the transaction
func AttachTxID(txid core.TransactionID, env AttachEnvironment) (vid *utangle_new.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		txid1 := txid
		vid = env.GetVertexNoLock(&txid1)
		if vid == nil {
			vid = utangle_new.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			env.Pull(&txid1)
		}
	})
	return
}

// AttachTransaction ensures vertex of the transaction is on the utangle. If it finds TxID vertex, it converts it to regular vertex
// It panics if it finds other that TxID vertex
func AttachTransaction(tx *transaction.Transaction, env AttachEnvironment, ctx context.Context) (vid *utangle_new.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(tx.ID())
		if vid == nil {
			vid = utangle_new.NewVertex(tx).Wrap()
			env.AddVertexNoLock(vid)
		}
		panicRepeatedTx := func() { env.Log().Panicf("AttachTransaction: repeated transaction %s", tx.IDShortString()) }

		vid.Unwrap(utangle_new.UnwrapOptions{
			TxID: func(txid *core.TransactionID) {
				// replace TxID with regular vertex
				vid.PutVertex(utangle_new.NewVertex(tx))
			},
			Vertex:    func(_ *utangle_new.Vertex) { panicRepeatedTx() },
			VirtualTx: func(_ *utangle_new.VirtualTransaction) { panicRepeatedTx() },
			Deleted:   vid.PanicAccessDeleted,
		})
		if vid.IsSequencerMilestone() {
			// starts attacher goroutine for sequencer transactions.
			// The goroutine will doo al the heavy lifting of solidification, pulling, validation and notification
			go _attach(vid, env, ctx)
		}
	})
	return
}

func _attach(vid *utangle_new.WrappedTx, env AttachEnvironment, ctx context.Context) {
	success, allInputsAttached := initSolidification(vid, env)
	if !success {
		vid.SetTxStatus(utangle_new.TxStatusBad)
		vid.NotifyFutureCone()
		return
	}
	// TODO warning: good input tx does not mean output indices are valid

	a := newAttacher(vid, env, allInputsAttached)
	defer a.close()

	for exit := false; !exit; {
		if !vid.IsVertex() {
			// stop once it converted to virtualTx or deleted
			return
		}
		select {
		case <-ctx.Done():
			return

		case msg := <-a.inChan:
			if msg == nil {
				return
			}
			exit = !a.processNotification(msg)

		case <-time.After(periodicCheckEach):
			exit = !a.periodicCheck()
		}
	}
}

func initSolidification(vid *utangle_new.WrappedTx, env AttachEnvironment) (success, allInputsAttached bool) {
	vid.Unwrap(utangle_new.UnwrapOptions{
		Vertex: func(v *utangle_new.Vertex) {
			success, allInputsAttached = attachInputTransactionsIfNeeded(v, env)
		},
	})
	return
}

// attachInputTransactionsIfNeeded does not check correctness of output indices, only transaction status
func attachInputTransactionsIfNeeded(v *utangle_new.Vertex, env AttachEnvironment) (bool, bool) {
	var stemInputTxID, seqInputTxID core.TransactionID

	if v.Tx.IsBranchTransaction() {
		stemInputIdx := v.StemInputIndex()
		if v.Inputs[stemInputIdx] == nil {
			stemInputOid := v.Tx.MustInputAt(stemInputIdx)
			stemInputTxID = stemInputOid.TransactionID()
			v.Inputs[stemInputIdx] = AttachTxID(stemInputTxID, env)
		}
		switch v.Inputs[stemInputIdx].GetTxStatus() {
		case utangle_new.TxStatusBad:
			return false, false
		case utangle_new.TxStatusUndefined:
			return true, false
		}
	}
	// stem is good
	seqInputIdx := v.SequencerInputIndex()
	seqInputOid := v.Tx.MustInputAt(seqInputIdx)
	seqInputTxID = seqInputOid.TransactionID()
	v.Inputs[seqInputIdx] = AttachTxID(seqInputTxID, env)
	switch v.Inputs[seqInputIdx].GetTxStatus() {
	case utangle_new.TxStatusBad:
		return false, false
	case utangle_new.TxStatusUndefined:
		return true, false
	}
	// stem and seq inputs are ok. We can pull the rest
	missing := v.MissingInputTxIDSet().Remove(seqInputTxID)
	if v.Tx.IsBranchTransaction() {
		missing.Remove(stemInputTxID)
	}
	success := true
	v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		if v.Inputs[i] == nil {
			v.Inputs[i] = AttachTxID(oid.TransactionID(), env)
		}
		success = v.Inputs[i].GetTxStatus() != utangle_new.TxStatusBad
		return success
	})
	if !success {
		return false, false
	}
	v.Tx.ForEachEndorsement(func(idx byte, txid *core.TransactionID) bool {
		if v.Endorsements[idx] == nil {
			v.Endorsements[idx] = AttachTxID(*txid, env)
		}
		success = v.Endorsements[idx].GetTxStatus() != utangle_new.TxStatusBad
		return success
	})
	return success, success
}

func newAttacher(vid *utangle_new.WrappedTx, env AttachEnvironment, allInputsAttached bool) *attacher {
	util.Assertf(vid.IsSequencerMilestone(), "sequencer milestone expected")

	var numMissingTx uint16
	vid.Unwrap(utangle_new.UnwrapOptions{Vertex: func(v *utangle_new.Vertex) {
		numMissingTx = uint16(len(v.MissingInputTxIDSet()))
	}})

	inChan := make(chan any, 1)
	vid.OnNotify(func(msg any) {
		inChan <- msg
	})

	return &attacher{
		vid:               vid,
		env:               env,
		inChan:            inChan,
		numMissingTx:      numMissingTx,
		allInputsAttached: allInputsAttached,
	}
}

func (a *attacher) close() {
	a.vid.OnNotify(nil)
	close(a.inChan)
}

func (a *attacher) processNotification(msg any) bool {
	if util.IsNil(msg) {
		return false
	}
	switch m := msg.(type) {
	case *utangle_new.WrappedTx:
	}
}

func (a *attacher) periodicCheck() bool {

}
