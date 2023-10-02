package utangle

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

func (ut *UTXOTangle) GetWrappedOutput(oid *core.OutputID, getState ...func() multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
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
			return ut.wrapNewIntoExistingBranch(vid, oid)
		}
		return wrapNewIntoExistingNonBranch(vid, oid, getState...)
	}
	// transaction not on UTXO tangle
	if oid.BranchFlagON() {
		return ut.fetchAndWrapBranch(oid)
	}
	// non-branch not on the utxo tangle
	if len(getState) == 0 {
		// no info on input, maybe later
		return WrappedOutput{}, false, false
	}
	// looking for output in the provided state
	o, err := getState[0]().GetOutput(oid)
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
	bd, branchFound := multistate.FetchBranchDataByTransactionID(ut.stateStore, oid.TransactionID())
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

func wrapNewIntoExistingNonBranch(vid *WrappedTx, oid *core.OutputID, getState ...func() multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
	util.Assertf(!oid.BranchFlagON(), "%s should not be branch", oid.Short())
	// Don't have output in existing vertex, but it may be a virtualTx
	if len(getState) == 0 {
		return WrappedOutput{}, false, false
	}
	var ret WrappedOutput
	var available, invalid bool
	vid.Unwrap(UnwrapOptions{
		VirtualTx: func(v *VirtualTransaction) {
			o, err := getState[0]().GetOutput(oid)
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

			bd, branchFound := multistate.FetchBranchDataByTransactionID(ut.stateStore, oid.TransactionID())
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

// WrapOutput fetches output in encoded form. Creates VirtualTransaction vertex and branch, if necessary
func (ut *UTXOTangle) WrapOutput(o *core.OutputWithID) (WrappedOutput, bool) {
	txid := o.ID.TransactionID()

	if vid, found := ut.GetVertex(&txid); found {
		// the transaction is on the UTXO tangle, i.e. already wrapped
		available := true
		vid.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				if int(o.ID.Index()) > v.Tx.NumProducedOutputs() {
					available = false
				}
			},
			VirtualTx: func(v *VirtualTransaction) {
				v.outputs[o.ID.Index()] = o.Output
			},
			Orphaned: func() {
				available = false
			},
		})
		if available {
			return WrappedOutput{vid, o.ID.Index()}, true
		}
		return WrappedOutput{}, false
	}
	// the transaction is not on the tangle, i.e. not wrapped. Creating virtual tx for it
	return ut.WrapNewOutput(o)
}

// WrapNewOutput creates virtual transaction for transaction which is not on the UTXO tangle
func (ut *UTXOTangle) WrapNewOutput(o *core.OutputWithID) (WrappedOutput, bool) {
	txid := o.ID.TransactionID()
	if o.ID.BranchFlagON() {
		// the corresponding transaction is branch tx. It must exist in the state.
		// Reaching it out and wrapping it with chain and stem outputs
		bd, foundBranchData := multistate.FetchBranchDataByTransactionID(ut.stateStore, txid)
		if foundBranchData {
			ret := newVirtualBranchTx(&bd).Wrap()
			ut.AddVertexAndBranch(ret, bd.Root)
			return WrappedOutput{VID: ret, Index: o.ID.Index()}, true
		}
		return WrappedOutput{}, false
	}

	v := newVirtualTx(&txid)
	v.addOutput(o.ID.Index(), o.Output)
	ret := v.Wrap()
	ut.AddVertexNoSaveTx(ret)

	return WrappedOutput{VID: ret, Index: o.ID.Index()}, true
}

func (ut *UTXOTangle) MustWrapOutput(o *core.OutputWithID) WrappedOutput {
	ret, ok := ut.WrapOutput(o)
	util.Assertf(ok, "can't wrap output %s", func() any { return o.IDShort() })
	return ret
}

// solidifyOutput returns:
// - nil, nil if output cannot be solidified yet, but no error
// - nil, err if output cannot be solidified ever
// - vid, nil if solid reference has been found
func (ut *UTXOTangle) solidifyOutput(oid *core.OutputID, baseStateReader func() multistate.SugaredStateReader) (*WrappedTx, error) {
	txid := oid.TransactionID()
	ret, found := ut.GetVertex(&txid)
	if found {
		var err error
		ret.Unwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				if int(oid.Index()) >= v.Tx.NumProducedOutputs() {
					err = fmt.Errorf("wrong output %s", oid.Short())
				}
			},
			VirtualTx: func(v *VirtualTransaction) {
				_, err = v.ensureOutputAt(oid.Index(), baseStateReader)
			},
		})
		if err != nil {
			return nil, err
		}
		return ret, nil
	}

	o, err := baseStateReader().GetOutputWithID(oid)
	if errors.Is(err, multistate.ErrNotFound) {
		// error means nothing because output might not exist yet
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("solidifyOutput: %w", err)
	}
	wOut, success := ut.WrapNewOutput(o)
	if !success {
		return nil, fmt.Errorf("can't wrap %s", o.ID.Short())
	}
	return wOut.VID, nil
}

func (v *VirtualTransaction) ensureOutputAt(idx byte, stateReader func() multistate.SugaredStateReader) (*core.Output, error) {
	ret, ok := v.OutputAt(idx)
	if ok {
		return ret, nil
	}

	v.mutex.Lock()
	defer v.mutex.Unlock()

	oid := core.NewOutputID(&v.txid, idx)
	oData, found := stateReader().GetUTXO(&oid)
	if !found {
		return nil, fmt.Errorf("output not found in the state: %s", oid.Short())
	}
	o, err := core.OutputFromBytesReadOnly(oData)
	util.AssertNoError(err)
	v.outputs[idx] = o
	return o, nil
}

// FetchMissingDependencies check solidity of inputs and fetches what is available
// Does not obtain global lock on the tangle
// It means in general the result is non-deterministic, because some dependencies may be unavailable. This is ok for solidifier
// Once transaction has all dependencies solid, the result is deterministic
func (v *Vertex) FetchMissingDependencies(ut *UTXOTangle) error {
	var err error
	if v.Tx.IsSequencerMilestone() && v.StateDelta.baselineBranch == nil {
		if err = v.fetchBranchDependency(ut); err != nil {
			return err
		}
		if v.StateDelta.baselineBranch == nil {
			// not solid yet, can't continue with solidification of the sequencer tx
			return nil
		}
	}
	// ---- solidify inputs
	v.Tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		if v.Inputs[i] != nil {
			// it is already solid
			return true
		}
		v.Inputs[i], err = ut.solidifyOutput(oid, func() multistate.SugaredStateReader {
			return v.mustGetBaseState(ut)
		})
		return err == nil
	})
	if err != nil {
		return err
	}

	//----  solidify endorsements
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

func (v *Vertex) fetchBranchDependency(ut *UTXOTangle) error {
	// find a vertex which to follow towards branch transaction
	// If tx itself is a branch tx, it will point towards previous transaction in the sequencer chain
	// Each sequencer transaction belongs to a branch
	branchConeTipVertex, err := ut.getBranchConeTipVertex(v)
	if err != nil {
		// something wrong with the transaction
		return err
	}
	if branchConeTipVertex == nil {
		// the vertex has no solid root, cannot be solidified (yet or never)
		return nil
	}
	// vertex has solid branch
	util.Assertf(branchConeTipVertex.IsSequencerMilestone(), "branchConeTipVertex.Tx.IsSequencerMilestone()")

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
		util.Assertf(v.StateDelta.baselineBranch != nil, "v.Branch != nil")
	}
	return nil
}

// getBranchConeTipVertex for a sequencer transaction, it finds a vertex which is to follow towards
// the branch transaction
// Returns:
// - nil, nil if it is not solid
// - nil, err if input is wrong, i.e. it cannot be solidified
// - vertex, nil if vertex, the branch cone tip, has been found
func (ut *UTXOTangle) getBranchConeTipVertex(v *Vertex) (*WrappedTx, error) {
	util.Assertf(v.Tx.IsSequencerMilestone(), "tx.IsSequencerMilestone()")
	oid := v.Tx.SequencerChainPredecessorOutputID()
	if oid == nil {
		// this transaction is chain origin, i.e. it does not have predecessor
		// follow the first endorsement. It enforced by transaction constraint layer
		return ut.mustGetFirstEndorsedVertex(v.Tx), nil
	}
	// sequencer chain predecessor exists
	if oid.TimeSlot() == v.TimeSlot() {
		if oid.SequencerFlagON() {
			return ut.vertexByOutputID(oid)
		}
		return ut.mustGetFirstEndorsedVertex(v.Tx), nil
	}
	if v.Tx.IsBranchTransaction() {
		return ut.vertexByOutputID(oid)
	}
	return ut.mustGetFirstEndorsedVertex(v.Tx), nil
}

// vertexByOutputID returns nil if transaction is not on the tangle or orphaned. Error indicates wrong output index
func (ut *UTXOTangle) vertexByOutputID(oid *core.OutputID) (*WrappedTx, error) {
	txid := oid.TransactionID()
	ret, found := ut.GetVertex(&txid)
	if !found {
		return nil, nil
	}
	if _, err := ret.OutputWithIDAt(oid.Index()); err != nil {
		return nil, err
	}
	return ret, nil
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
