package vertex

//
//// GetWrappedOutput return a wrapped output either the one existing in the utangle_old,
//// or after finding it in the provided state.
//// It returns flag if output ID cannot be solidified (invalid), for example output index is wrong
//// It returns nil if output cannot be found with the data provided
//func (ut *dag.UTXOTangle) GetWrappedOutput(oid *core.OutputID, baselineState ...multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
//	ut.mutex.Lock()
//	defer ut.mutex.Unlock()
//
//	// first search vertex on the tangle and pick output from it. Fetch output from state if necessary
//	ret, found, invalid := ut._pickFromExistingVertex(oid, baselineState...)
//	if found || invalid {
//		return ret, found, invalid
//	}
//	// transaction is unknown as a vertex. Try to fetch it from the state and create a virtual tx
//
//	if oid.BranchFlagON() {
//		// it is on the branch transaction, so pick it from the database
//		return ut._fetchAndWrapBranch(oid)
//	}
//
//	// non-branch not on the utxo tangle ->
//	if len(baselineState) == 0 {
//		// no state provided, no information, maybe later
//		return WrappedOutput{}, false, false
//	}
//
//	// looking for the output in the provided state
//	txid := oid.TransactionID()
//	o, err := baselineState[0].GetOutputErr(oid)
//	if err != nil {
//		// error occurred while loading from the state
//		if errors.Is(err, multistate.ErrNotFound) {
//			// output is not on the state
//			// if transaction is known -> output is consumed and cannot be solidified
//			invalid = baselineState[0].KnowsCommittedTransaction(&txid)
//			if invalid {
//			}
//			return WrappedOutput{}, false, invalid
//		}
//		// some other error than ErrNotFound -> it is invalid
//		return WrappedOutput{}, false, true
//	}
//
//	// found on the state. Creating and wrapping new virtual tx
//	vt := newVirtualTx(&txid)
//	vt.addOutput(oid.Index(), o)
//	vid := vt.Wrap()
//	conflict := ut._attach(vid)
//	util.Assertf(conflict == nil, "inconsistency: unexpected conflict %s", conflict.IDShort())
//
//	return WrappedOutput{VID: vid, Index: oid.Index()}, true, false
//}
//
//// _pickFromExistingVertex returns wrapped output if vertex already in on the tangle
//// If output belongs to the virtual tx but is not cached there, loads it (if state is provided)
//func (ut *dag.UTXOTangle) _pickFromExistingVertex(oid *core.OutputID, baselineState ...multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
//	txid := oid.TransactionID()
//	if vid, found := ut._getVertex(&txid); found {
//		hasIt, invalid := vid.HasOutputAt(oid.Index())
//		if invalid {
//			return WrappedOutput{}, false, true
//		}
//		if hasIt {
//			return WrappedOutput{VID: vid, Index: oid.Index()}, true, false
//		}
//		// here it can only be a virtual tx
//		util.Assertf(vid.IsVirtualTx(), "virtual tx expected")
//
//		if oid.IsBranchTransaction() {
//			// it means a virtual branch vertex exist but the output is not cached on it.
//			// It won't be a seq or stem output, because those are cached always in the branch virtual tx
//			return ut._wrapNewIntoExistingVirtualBranch(vid, oid)
//		}
//		// it is a virtual tx, output not cached
//		return _wrapNewIntoExistingVirtualNonBranch(vid, oid, baselineState...)
//	}
//	return WrappedOutput{}, false, false
//}
//
//func (ut *dag.UTXOTangle) _wrapNewIntoExistingVirtualBranch(vid *WrappedTx, oid *core.OutputID) (WrappedOutput, bool, bool) {
//	util.Assertf(oid.BranchFlagON(), "%s should be a branch", oid.StringShort())
//
//	var ret WrappedOutput
//	var available, invalid bool
//
//	vid.Unwrap(UnwrapOptions{
//		VirtualTx: func(v *VirtualTransaction) {
//			_, already := v.OutputAt(oid.Index())
//			util.Assertf(!already, "inconsistency: output %s should not exist in the virtualTx", func() any { return oid.StringShort() })
//
//			bd, branchFound := multistate.FetchBranchData(ut.stateStore, oid.TransactionID())
//			util.Assertf(branchFound, "inconsistency: branch %s must exist", oid.StringShort())
//
//			rdr := multistate.MustNewSugaredStateReader(ut.stateStore, bd.Root)
//
//			o, err := rdr.GetOutputErr(oid)
//			if errors.Is(err, multistate.ErrNotFound) {
//				return // null, false, false
//			}
//			if err != nil {
//				invalid = true
//				return // null, false, true
//			}
//			v.addOutput(oid.Index(), o)
//			ret = WrappedOutput{VID: vid, Index: oid.Index()}
//			available = true
//			return // ret, true, false
//		},
//		Deleted: vid.PanicAccessDeleted,
//	})
//	return ret, available, invalid
//}
//
//func _wrapNewIntoExistingVirtualNonBranch(vid *WrappedTx, oid *core.OutputID, baselineState ...multistate.SugaredStateReader) (WrappedOutput, bool, bool) {
//	util.Assertf(!oid.BranchFlagON(), "%s should not be branch", oid.StringShort())
//	// Don't have output in existing vertex, but it may be a virtualTx
//	if len(baselineState) == 0 {
//		return WrappedOutput{}, false, false
//	}
//	var ret WrappedOutput
//	var available, invalid bool
//	vid.Unwrap(UnwrapOptions{
//		VirtualTx: func(v *VirtualTransaction) {
//			o, err := baselineState[0].GetOutputErr(oid)
//			if errors.Is(err, multistate.ErrNotFound) {
//				return // null, false, false
//			}
//			if err != nil {
//				invalid = true
//				return // null, false, true
//			}
//			v.addOutput(oid.Index(), o)
//			ret = WrappedOutput{VID: vid, Index: oid.Index()}
//			available = true
//			return // ret, true, false
//		},
//		Deleted: vid.PanicAccessDeleted,
//	})
//	return ret, available, invalid
//}
//
//func (ut *dag.UTXOTangle) _fetchAndWrapBranch(oid *core.OutputID) (WrappedOutput, bool, bool) {
//	// it is a branch tx output, fetch the whole branch
//	bd, branchFound := multistate.FetchBranchData(ut.stateStore, oid.TransactionID())
//	if !branchFound {
//		// maybe later
//		return WrappedOutput{}, false, false
//	}
//	// branch found. Create virtualTx with seq and stem outputs
//	vt := dag.NewVirtualBranchTx(&bd)
//	if oid.Index() != bd.SequencerOutput.ID.Index() && oid.Index() != bd.Stem.ID.Index() {
//		// not seq or stem
//		rdr := multistate.MustNewSugaredStateReader(ut.stateStore, bd.Root)
//		o, err := rdr.GetOutputErr(oid)
//		if err != nil {
//			// if the output cannot be fetched from the branch state, it does not exist
//			return WrappedOutput{}, false, true
//		}
//		vt.addOutput(oid.Index(), o)
//	}
//	vid := vt.Wrap()
//	ut._addVertexAndBranch(vid, bd.Root)
//	return WrappedOutput{VID: vid, Index: oid.Index()}, true, false
//}
