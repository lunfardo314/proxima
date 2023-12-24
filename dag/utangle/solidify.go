package utangle

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/dag/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

func (ut *UTXOTangle) MakeDraftVertexFromTxBytes(txBytes []byte) (*vertex.Vertex, error) {
	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	if err != nil {
		return nil, err
	}
	ret, conflict := ut.MakeDraftVertex(tx)
	if conflict != nil {
		return nil, fmt.Errorf("can't solidify %s due to conflict in the past cone %s", tx.IDShortString(), conflict.StringShort())
	}
	return ret, nil
}

func (ut *UTXOTangle) MakeDraftVertex(tx *transaction.Transaction) (*vertex.Vertex, *core.OutputID) {
	ret := NewVertex(tx)
	if conflict := ret.FetchMissingDependencies(ut); conflict != nil {
		return nil, conflict
	}
	return ret, nil
}

// FetchMissingDependencies checks solidity of inputs and fetches what is available
// In general, the result is non-deterministic because some dependencies may be unavailable. This is ok for solidifier
// Once transaction has all dependencies solid, further on the result is deterministic
func (v *vertex.Vertex) FetchMissingDependencies(ut *UTXOTangle) (conflict *core.OutputID) {
	if conflict = v.fetchMissingEndorsements(ut); conflict == nil {
		if baselineBranch := v.BaselineBranch(); baselineBranch != nil {
			conflict = v.fetchMissingInputs(ut, ut.MustGetSugaredStateReader(baselineBranch.ID()))
		} else {
			conflict = v.fetchMissingInputs(ut)
		}
	}
	if conflict != nil {
		return
	}
	if v._allEndorsementsSolid() && v._allInputsSolid() {
		v.pastTrack.forks.cleanDeleted()
		v.isSolid = true // fully solidified
	}
	return
}

func (v *vertex.Vertex) fetchMissingInputs(ut *UTXOTangle, baselineState ...multistate.SugaredStateReader) (conflict *core.OutputID) {
	var conflictWrapped *vertex.WrappedOutput
	v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		if v.Inputs[i] != nil {
			// it is already solid
			return true
		}
		inputWrapped, ok, invalid := ut.GetWrappedOutput(oid, baselineState...)
		if invalid {
			conflict = oid
			return false
		}
		if ok {
			if conflictWrapped = v.pastTrack.absorbPastTrack(inputWrapped.VID, ut.StateStore); conflictWrapped != nil {
				conflict = conflictWrapped.DecodeID()
				return false
			}
			v.Inputs[i] = inputWrapped.VID
		}
		return true
	})
	return
}

func (v *vertex.Vertex) fetchMissingEndorsements(ut *UTXOTangle) (conflict *core.OutputID) {
	var conflictWrapped *vertex.WrappedOutput

	v.Tx.ForEachEndorsement(func(i byte, txid *core.TransactionID) bool {
		if v.Endorsements[i] != nil {
			// already solid and merged
			return true
		}
		util.Assertf(v.Tx.TimeSlot() == txid.TimeSlot(), "tx.TimeTick() == txid.TimeTick()")
		if vEndorsement, found := ut.GetVertex(txid); found {
			if conflictWrapped = v.pastTrack.absorbPastTrack(vEndorsement, ut.StateStore); conflictWrapped != nil {
				conflict = conflictWrapped.DecodeID()
				return false
			}
			v.Endorsements[i] = vEndorsement
		}
		return true
	})
	return
}

// mergeBranches return <branch>, <success>
func mergeBranches(b1, b2 *vertex.WrappedTx, getStore func() global.StateStore) (*vertex.WrappedTx, bool) {
	switch {
	case b1 == b2:
		return b1, true
	case b1 == nil:
		return b2, true
	case b2 == nil:
		return b1, true
	case b1.TimeSlot() == b2.TimeSlot():
		// two different branches on the same slot conflicts
		return nil, false
	case b1.TimeSlot() > b2.TimeSlot():
		if isDesc := multistate.BranchIsDescendantOf(b1.ID(), b2.ID(), getStore); isDesc {
			return b1, true
		}
	default:
		if isDesc := multistate.BranchIsDescendantOf(b2.ID(), b1.ID(), getStore); isDesc {
			return b2, true
		}
	}
	return nil, false
}
