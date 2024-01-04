package attacher

import (
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
)

func (a *attacher) solidifyBaselineState() vertex.Status {
	return a.lazyRepeat(func() vertex.Status {
		var ok bool
		success := false
		a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			ok = a.solidifyBaseline(v)
			if ok && v.FlagsUp(vertex.FlagBaselineSolid) {
				a.baselineBranch = v.BaselineBranch
				success = true
			}
		}})
		switch {
		case !ok:
			return vertex.Bad
		case success:
			return vertex.Good
		default:
			return vertex.Undefined
		}
	})
}

// solidifyBaseline directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) solidifyBaseline(v *vertex.Vertex) (ok bool) {
	if v.Tx.IsBranchTransaction() {
		return a.solidifyStem(v)
	}
	return a.solidifySequencerBaseline(v)
}

func (a *attacher) solidifyStem(v *vertex.Vertex) (ok bool) {
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
		v.SetFlagUp(vertex.FlagBaselineSolid)
		return true
	case vertex.Bad:
		a.setReason(v.Inputs[stemInputIdx].GetReason())
		return false
	case vertex.Undefined:
		a.env.OnChangeNotify(v.Inputs[stemInputIdx], a.vid)
		return true
	default:
		panic("wrong vertex state")
	}
}

func (a *attacher) solidifySequencerBaseline(v *vertex.Vertex) (ok bool) {
	// regular sequencer tx. Go to the direction of the baseline branch
	predOid, predIdx := v.Tx.SequencerChainPredecessor()
	util.Assertf(predOid != nil, "inconsistency: sequencer milestone cannot be a chain origin")
	var inputTx *vertex.WrappedTx

	// follow the endorsement if it is cross-slot or predecessor is not sequencer tx
	followTheEndorsement := predOid.TimeSlot() != v.Tx.TimeSlot() || !predOid.IsSequencerTransaction()
	if followTheEndorsement {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		if v.Endorsements[0] == nil {
			v.Endorsements[0] = AttachTxID(v.Tx.EndorsementAt(0), a.env, true)
		}
		inputTx = v.Endorsements[0]
	} else {
		if v.Inputs[predIdx] == nil {
			v.Inputs[predIdx] = AttachTxID(predOid.TransactionID(), a.env, true)
			util.Assertf(v.Inputs[predIdx] != nil, "v.Inputs[predIdx] != nil")
		}
		inputTx = v.Inputs[predIdx]

	}
	switch inputTx.GetTxStatus() {
	case vertex.Good:
		v.BaselineBranch = inputTx.BaselineBranch()
		v.SetFlagUp(vertex.FlagBaselineSolid)
		util.Assertf(v.BaselineBranch != nil, "v.BaselineBranch!=nil")
		return true
	case vertex.Undefined:
		// vertex can be undefined but with correct baseline branch
		a.env.OnChangeNotify(inputTx, a.vid)
		return true
	case vertex.Bad:
		a.setReason(inputTx.GetReason())
		return false
	default:
		panic("wrong vertex state")
	}
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
