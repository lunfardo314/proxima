package attacher

import (
	"context"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
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
		StateStore() global.StateStore
		Pull(txid core.TransactionID)
	}

	attacher struct {
		inChan              chan any
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

// AttachTxID ensures the txid is on the utangle and it is pulled.
func AttachTxID(txid core.TransactionID, env AttachEnvironment) (vid *utangle_new.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		vid = env.GetVertexNoLock(&txid)
		if vid != nil {
			// found existing -> return it
			return
		}
		// it is new
		if !txid.BranchFlagON() {
			// if not branch -> just place the virtualTx on the utangle, no further action
			vid = utangle_new.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			return
		}
		// it is a branch transaction. Look up for the corresponding state
		bd, branchAvailable := multistate.FetchBranchData(env.StateStore(), txid)
		if !branchAvailable {
			// the corresponding state is not in the multistate DB -> put virtualTx to the utangle -> pull it
			// the puller will trigger further solidification by providing the transaction
			vid = utangle_new.WrapTxID(txid)
			env.AddVertexNoLock(vid)
			env.Pull(txid)
			return
		}
		// corresponding state has been found, it is solid -> put virtual branch tx with the state reader
		vid = utangle_new.NewVirtualBranchTx(&bd).Wrap()
		env.AddVertexNoLock(vid)
		rdr := multistate.MustNewReadable(env.StateStore(), bd.Root, maxStateReaderCacheSize)
		vid.SetBaselineStateReader(rdr)
	})
	return
}

// AttachTransaction attaches new incoming transaction. For sequencer transaction it starts attacher routine
// which manages solidification pull until transaction becomes solid or stopped by the context prescriptions
func AttachTransaction(tx *transaction.Transaction, env AttachEnvironment, ctx context.Context) (vid *utangle_new.WrappedTx) {
	env.WithGlobalWriteLock(func() {
		// look up for the txid
		vid = env.GetVertexNoLock(tx.ID())
		if vid == nil {
			// it is new. Create a new wrapped tx and put it to the utangle
			vid = utangle_new.NewVertex(tx).Wrap()
		} else {
			// it is existing. Must virtualTx -> replace virtual tx with the full transaction
			vid.MustConvertVirtualTxToVertex(utangle_new.NewVertex(tx))
		}
		env.AddVertexNoLock(vid)
		if vid.IsSequencerMilestone() {
			// starts attacher goroutine for sequencer transactions.
			go _attach(vid, env, ctx)
		}
	})
	return
}

func _attach(vid *utangle_new.WrappedTx, env AttachEnvironment, ctx context.Context) {
	if util.IsNil(vid.BaselineStateReader()) {
		if solidifyBaseline(vid, env) {

		}
	}
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
		if !solidifyBaseline(vid, env) {

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

func (a *attacher) solidifyBaseline() (valid bool) {
	valid = true
	if a.baselineStateReader != nil {
		return
	}
	var baselineStateReader global.IndexedStateReader

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
					baselineStateReader = v.Inputs[predIdx].BaselineStateReader() // may be nil
				}
				return
			}
			// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
			util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
			if v.Endorsements[0] == nil {
				v.Endorsements[0] = AttachTxID(v.Tx.EndorsementAt(0), a.env)
			}
			if valid = v.Endorsements[0].GetTxStatus() != utangle_new.TxStatusBad; valid {
				baselineStateReader = v.Endorsements[0].BaselineStateReader() // may be nil
			}
		},
		VirtualTx: a.vid.PanicShouldNotBeVirtualTx,
		Deleted:   a.vid.PanicAccessDeleted,
	})
	a.vid.SetBaselineStateReader(baselineStateReader)
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
