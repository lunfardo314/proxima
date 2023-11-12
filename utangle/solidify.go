package utangle

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

func (ut *UTXOTangle) MakeDraftVertexFromTxBytes(txBytes []byte) (*Vertex, error) {
	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	if err != nil {
		return nil, err
	}
	ret, conflict := ut.MakeDraftVertex(tx)
	if conflict != nil {
		return nil, fmt.Errorf("can't solidify %s due to conflict in the past cone %s", tx.IDShort(), conflict.Short())
	}
	return ret, nil
}

func (ut *UTXOTangle) MakeDraftVertex(tx *transaction.Transaction) (*Vertex, *core.OutputID) {
	ret := NewVertex(tx)
	if conflict := ret.FetchMissingDependencies(ut); conflict != nil {
		return nil, conflict
	}
	return ret, nil
}

// getExistingWrappedOutput returns wrapped output if vertex already in on the tangle
// If output belongs to the virtual tx but is not cached there, loads it (if state is provided)
func (ut *UTXOTangle) getExistingWrappedOutput(oid *core.OutputID, baselineState ...multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	txid := oid.TransactionID()
	if vid, found := ut.getVertex(&txid); found {
		hasIt, invalid := vid.HasOutputAt(oid.Index())
		if invalid {
			return WrappedOutput{}, false, true
		}
		if hasIt {
			return WrappedOutput{VID: vid, Index: oid.Index()}, true, false
		}
		// here it can only be a virtual tx
		util.Assertf(vid.isVirtualTx(), "virtual tx expected")

		if oid.IsBranchTransaction() {
			// it means a virtual branch vertex exist but the output is not cached on it.
			// It won't be a seq or stem output, because those are cached always in the branch virtual tx
			return ut.wrapNewIntoExistingVirtualBranch(vid, oid)
		}
		// it is a virtual tx, output not cached
		return wrapNewIntoExistingVirtualNonBranch(vid, oid, baselineState...)
	}
	return WrappedOutput{}, false, false
}

func (ut *UTXOTangle) wrapNewIntoExistingVirtualBranch(vid *WrappedTx, oid *core.OutputID) (WrappedOutput, bool, bool) {
	util.Assertf(oid.BranchFlagON(), "%s should be a branch", oid.Short())

	var ret WrappedOutput
	var available, invalid bool

	vid.Unwrap(UnwrapOptions{
		VirtualTx: func(v *VirtualTransaction) {
			_, already := v.OutputAt(oid.Index())
			util.Assertf(!already, "inconsistency: output %s should not exist in the virtualTx", func() any { return oid.Short() })

			bd, branchFound := multistate.FetchBranchData(ut.stateStore, oid.TransactionID())
			util.Assertf(branchFound, "inconsistency: branch %s must exist", oid.Short())

			rdr := multistate.MustNewSugaredStateReader(ut.stateStore, bd.Root)

			o, err := rdr.GetOutput(oid)
			if errors.Is(err, multistate.ErrNotFound) {
				return // null, false, false
			}
			if err != nil {
				invalid = true
				return // null, false, true
			}
			v.addOutput(oid.Index(), o)
			ret = WrappedOutput{VID: vid, Index: oid.Index()}
			available = true
			return // ret, true, false
		},
		Deleted: PanicDeleted,
	})
	return ret, available, invalid
}

func wrapNewIntoExistingVirtualNonBranch(vid *WrappedTx, oid *core.OutputID, baselineState ...multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
	util.Assertf(!oid.BranchFlagON(), "%s should not be branch", oid.Short())
	// Don't have output in existing vertex, but it may be a virtualTx
	if len(baselineState) == 0 {
		return WrappedOutput{}, false, false
	}
	var ret WrappedOutput
	var available, invalid bool
	vid.Unwrap(UnwrapOptions{
		VirtualTx: func(v *VirtualTransaction) {
			o, err := baselineState[0].GetOutput(oid)
			if errors.Is(err, multistate.ErrNotFound) {
				return // null, false, false
			}
			if err != nil {
				invalid = true
				return // null, false, true
			}
			v.addOutput(oid.Index(), o)
			ret = WrappedOutput{VID: vid, Index: oid.Index()}
			available = true
			return // ret, true, false
		},
		Deleted: PanicDeleted,
	})
	return ret, available, invalid
}

// GetWrappedOutput return a wrapped output either the one existing in the utangle,
// or after finding it in the provided state
func (ut *UTXOTangle) GetWrappedOutput(oid *core.OutputID, baselineState ...multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
	ret, found, invalid := ut.getExistingWrappedOutput(oid, baselineState...)
	if found || invalid {
		return ret, found, invalid
	}

	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	// transaction not on UTXO tangle
	if oid.BranchFlagON() {
		return ut.fetchAndWrapBranch(oid)
	}
	// non-branch not on the utxo tangle
	if len(baselineState) == 0 {
		// no info on input, maybe later
		return WrappedOutput{}, false, false
	}
	// looking for output in the provided state
	o, err := baselineState[0].GetOutput(oid)
	if err != nil {
		return WrappedOutput{}, false, !errors.Is(err, multistate.ErrNotFound)
	}
	// found. Creating and wrapping new virtual tx
	txid := oid.TransactionID()
	vt := newVirtualTx(&txid)
	vt.addOutput(oid.Index(), o)
	vid := vt.Wrap()
	conflict := ut.attach(vid)
	util.Assertf(conflict == nil, "inconsistency: unexpected conflict %s", conflict.IDShort())

	return WrappedOutput{VID: vid, Index: oid.Index()}, true, false
}

func (ut *UTXOTangle) fetchAndWrapBranch(oid *core.OutputID) (WrappedOutput, bool, bool) {
	// it is a branch tx output, fetch the whole branch
	bd, branchFound := multistate.FetchBranchData(ut.stateStore, oid.TransactionID())
	if !branchFound {
		// maybe later
		return WrappedOutput{}, false, false
	}
	// branch found. Create virtualTx with seq and stem outputs
	vt := newVirtualBranchTx(&bd)
	if oid.Index() != bd.SequencerOutput.ID.Index() && oid.Index() != bd.Stem.ID.Index() {
		// not seq or stem
		rdr := multistate.MustNewSugaredStateReader(ut.stateStore, bd.Root)
		o, err := rdr.GetOutput(oid)
		if err != nil {
			// if the output cannot be fetched from the branch state, it does not exist
			return WrappedOutput{}, false, true
		}
		vt.addOutput(oid.Index(), o)
	}
	vid := vt.Wrap()
	ut.addVertexAndBranch(vid, bd.Root)
	return WrappedOutput{VID: vid, Index: oid.Index()}, true, false
}

// FetchMissingDependencies check solidity of inputs and fetches what is available
// In general, the result is non-deterministic because some dependencies may be unavailable. This is ok for solidifier
// Once transaction has all dependencies solid, further on the result is deterministic
func (v *Vertex) FetchMissingDependencies(ut *UTXOTangle) (conflict *core.OutputID) {
	if conflict = v.fetchMissingEndorsements(ut); conflict == nil {
		conflict = v.fetchMissingInputs(ut)
	}
	if v._isSolid() {
		v.pastTrack.forks.cleanDeleted()
		v.isSolid = true
	}
	return
}

func (v *Vertex) fetchMissingInputs(ut *UTXOTangle) (conflict *core.OutputID) {
	var baselineStateArgs []multistate.SugaredStateReader
	if baselineBranch := v.BaselineBranch(); baselineBranch != nil {
		baselineStateArgs = []multistate.SugaredStateReader{ut.MustGetSugaredStateReader(baselineBranch.ID())}
	}

	var conflictWrapped *WrappedOutput
	v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		if v.Inputs[i] != nil {
			// it is already solid
			return true
		}
		inputWrapped, ok, invalid := ut.GetWrappedOutput(oid, baselineStateArgs...)
		if invalid {
			conflict = oid
			return false
		}
		if ok {
			if conflictWrapped = v.pastTrack.absorbPastTrack(inputWrapped.VID); conflictWrapped != nil {
				conflict = conflictWrapped.DecodeID()
				return false
			}
			v.Inputs[i] = inputWrapped.VID
		}
		return true
	})
	return
}

func (v *Vertex) fetchMissingEndorsements(ut *UTXOTangle) (conflict *core.OutputID) {
	var conflictWrapped *WrappedOutput

	v.Tx.ForEachEndorsement(func(i byte, txid *core.TransactionID) bool {
		if v.Endorsements[i] != nil {
			// already solid and merged
			return true
		}
		util.Assertf(v.Tx.TimeSlot() == txid.TimeSlot(), "tx.TimeTick() == txid.TimeTick()")
		if vEndorsement, found := ut.GetVertex(txid); found {
			if conflictWrapped = v.pastTrack.absorbPastTrack(vEndorsement); conflictWrapped != nil {
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
func mergeBranches(b1, b2 *WrappedTx) (*WrappedTx, bool) {
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
		if isDesc := b1.isDescendantBranchOf(b2); isDesc {
			return b1, true
		}
	default:
		if isDesc := b2.isDescendantBranchOf(b1); isDesc {
			return b2, true
		}
	}
	return nil, false
}

func (vid *WrappedTx) isDescendantBranchOf(vidPred *WrappedTx) bool {
	util.Assertf(vid != nil && vidPred != nil && vid.IsBranchTransaction() && vidPred.IsBranchTransaction(), "isDescendantBranchOf: must be a branch tx")
	return vid._isDescendantBranchOf(vidPred)
}

func (vid *WrappedTx) _isDescendantBranchOf(vidPred *WrappedTx) (isDesc bool) {
	if vid.TimeSlot() <= vidPred.TimeSlot() {
		return
	}
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		_, stemOutputIdx := v.Tx.SequencerAndStemOutputIndices()
		next := v.Inputs[stemOutputIdx]
		if isDesc = next == vidPred; !isDesc {
			isDesc = next._isDescendantBranchOf(vidPred)
		}
	}})
	return
}
