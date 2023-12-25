package attacher

import (
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
)

func (a *attacher) solidifyBaselineState() vertex.Status {
	return a.lazyRepeat(func() (status vertex.Status) {
		status = vertex.Bad
		a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			if a.baselineBranch == nil {
				status = a.solidifyBaseline(v)
			}
		}})
		if a.baselineBranch != nil {
			return vertex.Good
		}
		return status
	})
}

// solidifyBaseline directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) solidifyBaseline(v *vertex.Vertex) (status vertex.Status) {
	if v.Tx.IsBranchTransaction() {
		status = a.solidifyStem(v)
	} else {
		status = a.solidifySequencerBaseline(v)
	}
	a.baselineBranch = v.BaselineBranch
	return
}

func (a *attacher) solidifyStem(v *vertex.Vertex) vertex.Status {
	stemInputIdx := v.StemInputIndex()
	if v.Inputs[stemInputIdx] == nil {
		// predecessor stem is pending
		stemInputOid := v.Tx.MustInputAt(stemInputIdx)
		v.Inputs[stemInputIdx] = AttachTxID(stemInputOid.TransactionID(), a.env, false)
	}
	util.Assertf(v.Inputs[stemInputIdx] != nil, "v.Inputs[stemInputIdx] != nil")

	status := v.Inputs[stemInputIdx].GetTxStatus()
	switch status {
	case vertex.Good:
		v.BaselineBranch = v.Inputs[stemInputIdx].BaselineBranch()
		util.Assertf(v.BaselineBranch != nil, "a.baselineBranch != nil")
		return vertex.Good
	case vertex.Bad:
	case vertex.Undefined:
		a.env.OnChangeNotify(v.Inputs[stemInputIdx], a.vid)
	default:
		panic("wrong state")
	}
	return status
}

func (a *attacher) solidifySequencerBaseline(v *vertex.Vertex) vertex.Status {
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
	status := inputTx.GetTxStatus()
	switch status {
	case vertex.Good:
		v.BaselineBranch = inputTx.BaselineBranch() // may be nil
	case vertex.Bad:
	case vertex.Undefined:
		a.env.OnChangeNotify(inputTx, a.vid)
	}
	return status
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
