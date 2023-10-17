package utangle

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

func (ut *UTXOTangle) SolidifyInputsFromTxBytes(txBytes []byte) (*Vertex, error) {
	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	if err != nil {
		return nil, err
	}
	return ut.SolidifyInputs(tx)
}

func (ut *UTXOTangle) SolidifyInputs(tx *transaction.Transaction) (*Vertex, error) {
	ret := NewVertex(tx)
	if err := ret.FetchMissingDependencies(ut); err != nil {
		return nil, err
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
		util.Assertf(vid.IsVirtualTx(), "virtual tx expected")

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
		Orphaned: PanicOrphaned,
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
		Orphaned: PanicOrphaned,
	})
	return ret, available, invalid
}

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
	ut.addVertex(vid)

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
	if oid.Index() != bd.SeqOutput.ID.Index() && oid.Index() != bd.Stem.ID.Index() {
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
	ut.AddVertexAndBranch(vid, bd.Root)
	return WrappedOutput{VID: vid, Index: oid.Index()}, true, false
}

// FetchMissingDependencies check solidity of inputs and fetches what is available
// Does not obtain global lock on the tangle
// It means in general the result is non-deterministic, because some dependencies may be unavailable. This is ok for solidifier
// Once transaction has all dependencies solid, further on the result is deterministic
func (v *Vertex) FetchMissingDependencies(ut *UTXOTangle) error {
	v.fetchMissingEndorsements(ut)

	if v.StateDelta.branchTxID == nil {
		if err := v.fetchMissingInputs(ut); err != nil {
			return err
		}
	}

	if v.Tx.IsSequencerMilestone() {
		// baseline state must ultimately be determined for milestone
		baselineBranchID, conflict := v.getInputBaselineBranchID()

		if conflict {
			return fmt.Errorf("conflicting branches among inputs of %s", v.Tx.IDShort())
		}
		v.StateDelta.branchTxID = baselineBranchID
		if !v.IsSolid() && baselineBranchID != nil {
			// if still not solid, try to fetch remaining inputs with baseline state
			if err := v.fetchMissingInputs(ut, ut.MustGetSugaredStateReader(baselineBranchID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *Vertex) fetchMissingInputs(ut *UTXOTangle, baselineState ...multistate.SugaredStateReader) error {
	var err error
	var ok, invalid bool
	var wOut WrappedOutput

	v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		if v.Inputs[i] != nil {
			// it is already solid
			return true
		}
		wOut, ok, invalid = ut.GetWrappedOutput(oid, baselineState...)
		if invalid {
			err = fmt.Errorf("wrong output %s", oid.Short())
			return false
		}
		if ok {
			v.Inputs[i] = wOut.VID
		}
		return true
	})
	return err
}

func (v *Vertex) fetchMissingEndorsements(ut *UTXOTangle) {
	v.Tx.ForEachEndorsement(func(i byte, txid *core.TransactionID) bool {
		if v.Endorsements[i] != nil {
			// already solid
			return true
		}
		util.Assertf(v.Tx.TimeSlot() == txid.TimeSlot(), "tx.TimeTick() == txid.TimeTick()")
		if vEnd, solid := ut.GetVertex(txid); solid {
			util.Assertf(vEnd.IsSequencerMilestone(), "vEnd.IsSequencerMilestone()")
			v.Endorsements[i] = vEnd
		}
		return true
	})
}

// getInputBaselineBranchID scans known (solid) inputs and extracts baseline branch ID. Returns:
// - conflict == true if inputs belongs to conflicting branches
// - nil, false if known inputs does not give a common baseline (yet)
// - txid, false if known inputs has latest branchID (even if not all solid yet)
func (v *Vertex) getInputBaselineBranchID() (ret *core.TransactionID, conflict bool) {
	branchIDsBySlot := make(map[core.TimeSlot]*core.TransactionID)
	v.forEachDependency(func(inp *WrappedTx) bool {
		if inp == nil {
			return true
		}
		branchTxID := inp.DeltaBranchID()
		if branchTxID == nil {
			return true
		}
		slot := branchTxID.TimeSlot()
		if branchTxID1, already := branchIDsBySlot[slot]; already {
			if *branchTxID != *branchTxID1 {
				// two different branches in the same slot -> conflict
				conflict = true
				return false
			}
		} else {
			branchIDsBySlot[slot] = branchTxID
		}
		return true
	})
	if conflict {
		return
	}
	if len(branchIDsBySlot) == 0 {
		return
	}
	ret = util.Maximum(util.Values(branchIDsBySlot), func(branchTxID1, branchTxID2 *core.TransactionID) bool {
		return branchTxID1.TimeSlot() < branchTxID2.TimeSlot()
	})
	return
}

// getBranchConeTipVertex for a sequencer transaction, it finds a vertex which is to follow towards
// the branch transaction
// Returns:
// - nil, nil if it is not solid
// - nil, err if input is wrong, i.e. it cannot be solidified
// - vertex, nil if vertex, the branch cone tip, has been found
func (ut *UTXOTangle) getBranchConeTipVertex(tx *transaction.Transaction) (*WrappedTx, error) {
	util.Assertf(tx.IsSequencerMilestone(), "tx.IsSequencerMilestone()")
	oid := tx.SequencerChainPredecessorOutputID()
	if oid == nil {
		// this transaction is chain origin, i.e. it does not have predecessor
		// follow the first endorsement. It enforced by transaction constraint layer
		return ut.mustGetFirstEndorsedVertex(tx), nil
	}
	// sequencer chain predecessor exists
	if oid.TimeSlot() == tx.TimeSlot() {
		if oid.SequencerFlagON() {
			ret, ok, invalid := ut.GetWrappedOutput(oid)
			if invalid {
				return nil, fmt.Errorf("wrong output %s", oid.Short())
			}
			if !ok {
				return nil, nil
			}
			return ret.VID, nil
		}
		return ut.mustGetFirstEndorsedVertex(tx), nil
	}
	if tx.IsBranchTransaction() {
		ret, ok, invalid := ut.GetWrappedOutput(oid)
		if invalid {
			return nil, fmt.Errorf("wrong output %s", oid.Short())
		}
		if !ok {
			return nil, nil
		}
		return ret.VID, nil
	}
	return ut.mustGetFirstEndorsedVertex(tx), nil
}

// mustGetFirstEndorsedVertex returns first endorsement or nil if not solid
func (ut *UTXOTangle) mustGetFirstEndorsedVertex(tx *transaction.Transaction) *WrappedTx {
	util.Assertf(tx.NumEndorsements() > 0, "tx.NumEndorsements() > 0 @ %s", func() any { return tx.IDShort() })
	txid := tx.EndorsementAt(0)
	if ret, ok := ut.GetVertex(&txid); ok {
		return ret
	}
	// not solid
	return nil
}
