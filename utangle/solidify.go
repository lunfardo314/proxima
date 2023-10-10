package utangle

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func (ut *UTXOTangle) GetWrappedOutput(oid *core.OutputID, baselineState ...multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
	txid := oid.TransactionID()
	if vid, found := ut.GetVertex(&txid); found {
		hasIt, invalid := vid.HasOutputAt(oid.Index())
		if invalid {
			return WrappedOutput{}, false, true
		}
		if hasIt {
			return WrappedOutput{VID: vid, Index: oid.Index()}, true, false
		}
		if oid.IsBranchTransaction() {
			// it means a virtual branch vertex exist but the output is not cached on it.
			// It won't be a seq or stem output
			return ut.wrapNewIntoExistingBranch(vid, oid)
		}
		// it is a virtual tx, output not cached
		return wrapNewIntoExistingNonBranch(vid, oid, baselineState...)
	}
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
	vt := newVirtualTx(&txid)
	vt.addOutput(oid.Index(), o)
	vid := vt.Wrap()
	ut.AddVertexNoSaveTx(vid)

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

func wrapNewIntoExistingNonBranch(vid *WrappedTx, oid *core.OutputID, baselineState ...multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
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
	})
	return ret, available, invalid
}

func (ut *UTXOTangle) wrapNewIntoExistingBranch(vid *WrappedTx, oid *core.OutputID) (WrappedOutput, bool, bool) {
	util.Assertf(oid.BranchFlagON(), "%s should be a branch", oid.Short())

	var ret WrappedOutput
	var available, invalid bool

	vid.Unwrap(UnwrapOptions{
		Vertex: func(v *Vertex) {
			util.Panicf("should be a virtualTx %s", oid.Short())
		},
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
		Orphaned: func() {
			util.Panicf("should be a virtualTx %s", oid.Short())
		},
	})
	return ret, available, invalid
}

// solidifyOutput returns:
// - nil, nil if output cannot be solidified yet, but no error
// - nil, err if output cannot be solidified ever
// - vid, nil if solid reference has been found
func (ut *UTXOTangle) solidifyOutput(oid *core.OutputID, baseStateReader func() multistate.SugaredStateReader) (*WrappedTx, error) {
	ret, ok, invalid := ut.GetWrappedOutput(oid, baseStateReader)
	if invalid {
		return nil, fmt.Errorf("output %s cannot be solidified", oid.Short())
	}
	if !ok {
		return nil, nil
	}
	return ret.VID, nil
}

// FetchMissingDependencies check solidity of inputs and fetches what is available
// Does not obtain global lock on the tangle
// It means in general the result is non-deterministic, because some dependencies may be unavailable. This is ok for solidifier
// Once transaction has all dependencies solid, further on the result is deterministic
func (v *Vertex) FetchMissingDependencies(ut *UTXOTangle) error {
	if v.Tx.IsSequencerMilestone() {
		// baseline state must ultimately be determined
		baselineState, conflict := v.getInputBaselineState(ut.MustGetSugaredStateReader)
		if conflict {
			return fmt.Errorf("conflicting inputs in %s", v.Tx.IDShort())
		}
		if baselineState != nil {
			return v.fetchMissingSequencerDependencies(ut, *baselineState)
		}
		return v.fetchMissingSequencerDependencies(ut)
	}
	// not a sequencer transaction, baseline state does not exist
	return v.fetchMissingNonSequencerDependencies(ut)
}

func (v *Vertex) fetchMissingSequencerDependencies(ut *UTXOTangle, baselineState ...multistate.SugaredStateReader) error {
	if err := v.fetchMissingInputs(ut, baselineState...); err != nil {
		return err
	}
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
	return nil
}

func (v *Vertex) fetchMissingNonSequencerDependencies(ut *UTXOTangle) error {
	// TODO search outputs in several roots?
	baselineState := ut.HeaviestStateForLatestTimeSlot()
	return v.fetchMissingInputs(ut, baselineState)
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

// getInputBaselineState returns baseline state of inputs if it exists.
// Return conflict flag if inputs belongs to conflicting branches
// Note, that not all inputs may not be solid. the baseline branch is the latest among not-nil branches
// In non-conflicting case return nil if all known inputs are state-independent
func (v *Vertex) getInputBaselineState(getStateReader func(branchTxID *core.TransactionID) multistate.SugaredStateReader) (ret *multistate.SugaredStateReader, conflict bool) {
	branchIDs := set.New[core.TransactionID]()
	v.forEachInputDependency(func(i byte, inp *WrappedTx) bool {
		if inp == nil {
			return true
		}
		if branchTxID := inp.BaseBranchTXID(); branchTxID != nil {
			branchIDs.Insert(*branchTxID)
		}
		return true
	})
	v.forEachEndorsement(func(i byte, vEnd *WrappedTx) bool {
		if vEnd == nil {
			return true
		}
		if branchTxID := vEnd.BaseBranchTXID(); branchTxID != nil {
			branchIDs.Insert(*branchTxID)
		}
		return true
	})
	if len(branchIDs) == 0 {
		return
	}
	branchIDsSorted := branchIDs.Ordered(func(branchTxID1, branchTxID2 core.TransactionID) bool {
		return branchTxID1.TimeSlot() > branchTxID2.TimeSlot()
	})
	// check for conflicting branches
	for i, txid := range branchIDsSorted {
		if i+1 >= len(branchIDs) {
			break
		}
		txid1 := branchIDsSorted[i+1]
		if txid.TimeSlot() == txid1.TimeSlot() && txid != txid1 {
			// two different branches on the same slot are conflicting
			conflict = true
			return
		}
	}
	rdr := getStateReader(&branchIDsSorted[0])
	ret = &rdr
	return
}

func (v *Vertex) fetchBranchDependency(ut *UTXOTangle) error {
	// find a vertex which to follow towards branch transaction
	// If tx itself is a branch tx, it will point towards previous transaction in the sequencer chain
	// Each sequencer transaction belongs to a branch
	branchConeTipVertex, err := ut.getBranchConeTipVertex(v.Tx)
	if err != nil {
		// something wrong with the transaction
		return err
	}
	if branchConeTipVertex == nil {
		// the vertex has no solid root, cannot be solidified (yet or never)
		return nil
	}
	// vertex has solid branch tip (the state baseline can still be mil
	v.BranchConeTipSolid = true
	//util.Assertf(branchConeTipVertex.IsSequencerMilestone(), "expected branch cone tip %s to be a sequencer tx",
	//	branchConeTipVertex.LazyIDShort())

	if branchConeTipVertex.IsBranchTransaction() {
		util.Assertf(ut.isValidBranch(branchConeTipVertex), "ut.isValidBranch(branchConeTipVertex)")
		v.StateDelta.baselineBranch = branchConeTipVertex
	} else {
		// inherit branch root
		branchConeTipVertex.Unwrap(UnwrapOptions{
			Vertex: func(vUnwrap *Vertex) {
				v.StateDelta.baselineBranch = vUnwrap.StateDelta.baselineBranch
			},
		})
		//util.Assertf(v.StateDelta.baselineBranch != nil, "\n-- vertex: %s\n-- branchConeTipVertex: %s\n-- baseline branch: nil (unexpected)",
		//	v.Tx.IDShort(), func() any { return branchConeTipVertex.String() })
	}
	return nil
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
