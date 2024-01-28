package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

func newPastConeAttacher(env Environment, name string) attacher {
	return attacher{
		Environment:           env,
		name:                  name,
		rooted:                make(map[*vertex.WrappedTx]set.Set[byte]),
		definedPastVertices:   set.New[*vertex.WrappedTx](),
		undefinedPastVertices: set.New[*vertex.WrappedTx](),
		pokeMe:                func(_ *vertex.WrappedTx) {},
	}
}

const (
	TraceTagAttach       = "attach"
	TraceTagAttachOutput = "attachOutput"
	TraceTagAttachVertex = "attachVertexUnwrapped"
)

func (a *attacher) Name() string {
	return a.name
}

func (a *attacher) GetReason() error {
	return a.reason
}

func (a *attacher) baselineStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(a.GetStateReaderForTheBranch(a.baselineBranch))
}

func (a *attacher) setReason(err error) {
	a.Tracef(TraceTagAttach, "set reason: '%v'", err)
	a.reason = err
}

const TraceTagMarkDefUndef = "markDefUndef"

func (a *attacher) markVertexDefined(vid *vertex.WrappedTx) {
	delete(a.undefinedPastVertices, vid)
	a.definedPastVertices.Insert(vid)

	a.Tracef(TraceTagAttach, "markVertexDefined%s: %s is DEFINED", a.name, vid.IDShortString)
	a.Tracef(TraceTagMarkDefUndef, "markVertexDefined%s: %s is DEFINED", a.name, vid.IDShortString)
}

func (a *attacher) markVertexUndefined(vid *vertex.WrappedTx) {
	util.Assertf(!a.definedPastVertices.Contains(vid), "!a.definedPastVertices.Contains(vid)")
	a.undefinedPastVertices.Insert(vid)

	a.Tracef(TraceTagAttach, "markVertexUndefined%s: %s is UNDEFINED", a.name, vid.IDShortString)
	a.Tracef(TraceTagMarkDefUndef, "markVertexUndefined%s: %s is UNDEFINED", a.name, vid.IDShortString)
}

func (a *attacher) isKnownDefined(vid *vertex.WrappedTx) bool {
	if a.definedPastVertices.Contains(vid) {
		util.Assertf(!a.undefinedPastVertices.Contains(vid), "!a.undefinedPastVertices.Contains(vid)")
		return true
	}
	return false
}

func (a *attacher) isKnownUndefined(vid *vertex.WrappedTx) bool {
	if a.undefinedPastVertices.Contains(vid) {
		util.Assertf(!a.definedPastVertices.Contains(vid), "!a.definedPastVertices.Contains(vid)")
		return true
	}
	return false
}

func (a *attacher) isKnownNotRooted(vid *vertex.WrappedTx) (yes bool) {
	yes = a.isKnownDefined(vid) || a.isKnownUndefined(vid)
	if yes {
		_, found := a.rooted[vid]
		util.Assertf(!found, "inconsistency: transaction should not be marked rooted")
	}
	return
}

func (a *attacher) isKnownRooted(vid *vertex.WrappedTx) (yes bool) {
	_, yes = a.rooted[vid]
	return
}

func (a *attacher) isRootedOutput(wOut vertex.WrappedOutput) bool {
	rootedIndices := a.rooted[wOut.VID]
	if len(rootedIndices) == 0 {
		return false
	}
	util.Assertf(!a.isKnownNotRooted(wOut.VID), "!a.isKnownNotRooted(wOut.VID)")
	return rootedIndices.Contains(wOut.Index)
}

// solidifyBaselineVertex directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) solidifyBaselineVertex(v *vertex.Vertex) (ok bool) {
	if v.Tx.IsBranchTransaction() {
		return a.solidifyStemOfTheVertex(v)
	}
	return a.solidifySequencerBaseline(v)
}

func (a *attacher) solidifyStemOfTheVertex(v *vertex.Vertex) (ok bool) {
	stemInputIdx := v.StemInputIndex()
	if v.Inputs[stemInputIdx] == nil {
		// predecessor stem is pending
		stemInputOid := v.Tx.MustInputAt(stemInputIdx)
		v.Inputs[stemInputIdx] = AttachTxID(stemInputOid.TransactionID(), a, OptionInvokedBy(a.name))
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
		a.pokeMe(v.Inputs[stemInputIdx])
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
			v.Endorsements[0] = AttachTxID(v.Tx.EndorsementAt(0), a, OptionPullNonBranch, OptionInvokedBy(a.name))
		}
		inputTx = v.Endorsements[0]
	} else {
		if v.Inputs[predIdx] == nil {
			v.Inputs[predIdx] = AttachTxID(predOid.TransactionID(), a, OptionPullNonBranch, OptionInvokedBy(a.name))
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
		a.pokeMe(inputTx)
		return true
	case vertex.Bad:
		a.setReason(inputTx.GetReason())
		return false
	default:
		panic("wrong vertex state")
	}
}

// attachVertexUnwrapped: vid corresponds to the vertex v
// it solidifies vertex by traversing the past cone down to rooted tagAlongInputs or undefined vertices
// Repetitive calling of the function reaches all past vertices down to the rooted tagAlongInputs in the definedPastVertices set, while
// the undefinedPastVertices set becomes empty This is the exit condition of the loop.
// It results in all definedPastVertices are vertex.Good
// Otherwise, repetition reaches conflict (double spend) or vertex.Bad vertex and exits
// Returns OK (= not bad)
func (a *attacher) attachVertexUnwrapped(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime) (ok bool) {
	util.Assertf(!v.Tx.IsSequencerMilestone() || v.FlagsUp(vertex.FlagBaselineSolid), "v.FlagsUp(vertex.FlagBaselineSolid) in %s", v.Tx.IDShortString)

	if vid.GetTxStatusNoLock() == vertex.Bad {
		a.setReason(vid.GetReasonNoLock())
		return false
	}

	a.Tracef(TraceTagAttachVertex, "%s", vid.IDShortString)
	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")

	if a.isKnownDefined(vid) {
		return true
	}
	// mark vertex in the past cone, undefined yet
	a.markVertexUndefined(vid)

	if !v.FlagsUp(vertex.FlagEndorsementsSolid) {
		a.Tracef(TraceTagAttachVertex, "endorsements not solid in %s\n", v.Tx.IDShortString())
		// depth-first along endorsements
		if !a.attachEndorsements(v, parasiticChainHorizon) { // <<< recursive
			// not ok -> abandon attacher
			return false
		}
	}
	if v.FlagsUp(vertex.FlagEndorsementsSolid) {
		util.AssertNoError(a.allEndorsementsDefined(v))
		a.Tracef(TraceTagAttachVertex, "endorsements (%d) are all solid in %s", v.Tx.NumEndorsements(), v.Tx.IDShortString)
	} else {
		a.Tracef(TraceTagAttachVertex, "endorsements (%d) NOT solid in %s", v.Tx.NumEndorsements(), v.Tx.IDShortString)
	}
	inputsOk := a.attachInputsOfTheVertex(v, vid, parasiticChainHorizon) // deep recursion
	if !inputsOk {
		return false
	}
	if v.FlagsUp(vertex.FlagAllInputsSolid) {
		// TODO nice-to-have optimization: constraints can be validated even before the vertex becomes good (solidified).
		//  It is enough to have all tagAlongInputs available, i.e. before full solidification of the past cone

		if err := v.ValidateConstraints(); err != nil {
			a.setReason(err)
			vid.SetTxStatusBadNoLock(err)
			a.Tracef(TraceTagAttachVertex, "constraint validation failed in %s: '%v'", vid.IDShortString(), err)
			return false
		}
		a.Tracef(TraceTagAttachVertex, "constraints has been validated OK: %s", v.Tx.IDShortString)
		if !v.Tx.IsSequencerMilestone() && !v.FlagsUp(vertex.FlagTxBytesPersisted) {
			// persist bytes of the valid non-sequencer transaction, if needed
			// non-sequencer transaction always have empty persistent metadata
			// sequencer transaction will be persisted upon finalization of the attacher
			a.AsyncPersistTxBytesWithMetadata(v.Tx.Bytes(), nil)
			v.SetFlagUp(vertex.FlagTxBytesPersisted)
			a.Tracef(TraceTagAttachVertex, "tx bytes persisted: %s", v.Tx.IDShortString)
		}
	}
	if v.FlagsUp(vertex.FlagsSequencerVertexCompleted) {
		a.markVertexDefined(vid)
	}
	a.Tracef(TraceTagAttachVertex, "return OK: %s", v.Tx.IDShortString)
	return true
}

// Attaches endorsements of the vertex
// Return OK (== not bad)
func (a *attacher) attachEndorsements(v *vertex.Vertex, parasiticChainHorizon ledger.LogicalTime) bool {
	a.Tracef(TraceTagAttachVertex, "attachEndorsements: %s", v.Tx.IDShortString)

	numUndefined := len(v.Endorsements)
	for i, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			vidEndorsed = AttachTxID(v.Tx.EndorsementAt(byte(i)), a, OptionPullNonBranch, OptionInvokedBy(a.name))
			v.Endorsements[i] = vidEndorsed
		}
		if a.isKnownDefined(vidEndorsed) {
			numUndefined--
			continue
		}
		a.markVertexUndefined(vidEndorsed)

		baselineBranch := vidEndorsed.BaselineBranch()
		if baselineBranch == nil {
			// baseline branch not solid yet, abandon traverse. No need for any pull because
			// endorsed sequencer milestones are solidified proactively by attachers
			return true
		}
		// baseline must be compatible with baseline of the attacher
		if !a.branchesCompatible(a.baselineBranch, baselineBranch) {
			a.setReason(fmt.Errorf("attachEndorsements: baseline %s of endorsement %s is incompatible with the baseline branch%s",
				baselineBranch.IDShortString(), vidEndorsed.IDShortString(), a.baselineBranch.IDShortString()))
			return false
		}
		// sanity check: only one branch transaction can be endorsed
		util.Assertf(!vidEndorsed.IsBranchTransaction() || baselineBranch == a.baselineBranch,
			"attachEndorsements: !vidEndorsed.IsBranchTransaction() || baselineBranch == a.baselineBranch")

		switch vidEndorsed.GetTxStatus() {
		case vertex.Bad:
			util.Assertf(!a.isKnownDefined(vidEndorsed), "attachEndorsements: !a.isKnownDefined(vidEndorsed)")
			a.setReason(vidEndorsed.GetReason())
			return false
		case vertex.Good:
			if vidEndorsed.IsBranchTransaction() {
				// good branch endorsements are always 'defined'
				a.markVertexDefined(vidEndorsed)
				numUndefined--
				continue
			}
		}
		// non-branch undefined milestone. Go deep recursively
		ok := true
		vidEndorsed.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			ok = a.attachVertexUnwrapped(v, vidEndorsed, parasiticChainHorizon) // <<<<<<<<<<< recursion
		}})
		if !ok {
			a.setReason(vidEndorsed.GetReason())
			return false
		}
		util.AssertNoError(a.reason)

		// check status again
		switch status := vidEndorsed.GetTxStatus(); status {
		case vertex.Good:
			// this endorsement is defined for the attacher
			a.markVertexDefined(vidEndorsed)
			numUndefined--
		case vertex.Undefined:
			a.pokeMe(vidEndorsed)
		default:
			a.Log().Fatalf("attacher %s: unexpected state of the vertex %s (%s)", a.name, vidEndorsed.IDShortString(), status.String())
		}
	}
	if numUndefined == 0 {
		a.Tracef(TraceTagAttachVertex, "endorsements are all good in %s", v.Tx.IDShortString())
		v.SetFlagUp(vertex.FlagEndorsementsSolid)
	}
	return true
}

func (a *attacher) attachInputsOfTheVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime) (ok bool) {
	a.Tracef(TraceTagAttachVertex, "attachInputsOfTheVertex in %s", vid.IDShortString)
	allInputsValidated := true
	notSolid := make([]byte, 0, v.Tx.NumInputs())
	var success bool
	for i := range v.Inputs {
		ok, success = a.attachInput(v, byte(i), vid, parasiticChainHorizon)
		if !ok {
			return false
		}
		if !success {
			allInputsValidated = false
			notSolid = append(notSolid, byte(i))
		}
	}
	if allInputsValidated {
		v.SetFlagUp(vertex.FlagAllInputsSolid)
		if !v.Tx.IsSequencerMilestone() {
			// poke all other which are waiting for this non-sequencer tx. If sequencer tx, it will poke other upon finalization
			a.PokeAllWith(vid)
		}
	} else {
		a.Tracef(TraceTagAttachVertex, "attachInputsOfTheVertex: not solid: in %s:\n%s", v.Tx.IDShortString(), _lazyStringSelectedInputs(v.Tx, notSolid))
	}
	return true
}

func _lazyStringSelectedInputs(tx *transaction.Transaction, indices []byte) func() string {
	return func() string {
		ret := lines.New("      ")
		for _, i := range indices {
			ret.Add("#%d %s", i, util.Ref(tx.MustInputAt(i)).StringShort())
		}
		return ret.String()
	}
}

func (a *attacher) attachInput(v *vertex.Vertex, inputIdx byte, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime) (ok, success bool) {
	a.Tracef(TraceTagAttachVertex, "attachInput #%d of %s", inputIdx, vid.IDShortString)
	if !a.attachInputID(v, vid, inputIdx) {
		a.Tracef(TraceTagAttachVertex, "bad input %d", inputIdx)
		return false, false
	}
	util.Assertf(v.Inputs[inputIdx] != nil, "v.Inputs[i] != nil")

	if parasiticChainHorizon == ledger.NilLogicalTime {
		// TODO revisit parasitic chain threshold because of syncing branches
		parasiticChainHorizon = ledger.MustNewLogicalTime(v.Inputs[inputIdx].Timestamp().Slot()-maxToleratedParasiticChainSlots, 0)
	}
	wOut := vertex.WrappedOutput{
		VID:   v.Inputs[inputIdx],
		Index: v.Tx.MustOutputIndexOfTheInput(inputIdx),
	}

	if !a.attachOutput(wOut, parasiticChainHorizon) {
		return false, false
	}
	success = a.isKnownDefined(v.Inputs[inputIdx]) || a.isRootedOutput(wOut)
	if success {
		a.Tracef(TraceTagAttachVertex, "input #%d (%s) solidified", inputIdx, wOut.IDShortString)
	}
	return true, success
}

func (a *attacher) isValidated(vid *vertex.WrappedTx) bool {
	return a.definedPastVertices.Contains(vid)
}

func (a *attacher) attachRooted(wOut vertex.WrappedOutput) (ok bool, isRooted bool) {
	a.Tracef(TraceTagAttachOutput, "attachRooted %s", wOut.IDShortString)
	if wOut.Timestamp().After(a.baselineBranch.Timestamp()) {
		// output is later than baseline -> can't be rooted in it
		return true, false
	}

	consumedRooted := a.rooted[wOut.VID]
	if consumedRooted.Contains(wOut.Index) {
		// it means it is already covered. The double-spend checks are done by attachInputID
		return true, true
	}
	stateReader := a.baselineStateReader()

	oid := wOut.DecodeID()
	txid := oid.TransactionID()
	if len(consumedRooted) == 0 && !stateReader.KnowsCommittedTransaction(&txid) {
		// it is not rooted in the baseline state, but it is fine
		return true, false
	}
	// transaction is known in the state -> check if output is in the state (i.e. not consumed yet)
	out := stateReader.GetOutput(oid)
	if out == nil {
		// output has not been found in the state -> Bad (already consumed)
		err := fmt.Errorf("output %s is already consumed in the baseline state %s", wOut.IDShortString(), a.baselineBranch.IDShortString())
		a.setReason(err)
		a.Tracef(TraceTagAttachOutput, "%v", err)
		return false, false

	}

	// output has been found in the state -> Good
	ensured := wOut.VID.EnsureOutput(wOut.Index, out)
	util.Assertf(ensured, "ensureOutput: internal inconsistency")
	if len(consumedRooted) == 0 {
		consumedRooted = set.New[byte](wOut.Index)
	} else {
		consumedRooted.Insert(wOut.Index)
	}
	a.rooted[wOut.VID] = consumedRooted
	a.markVertexDefined(wOut.VID)

	// this is new rooted output -> add to the coverage delta
	a.coverage.AddDelta(out.Amount())
	return true, true
}

func (a *attacher) attachOutput(wOut vertex.WrappedOutput, parasiticChainHorizon ledger.LogicalTime) bool {
	a.Tracef(TraceTagAttachOutput, "%s", wOut.IDShortString)
	ok, isRooted := a.attachRooted(wOut)
	if !ok {
		return false
	}
	if isRooted {
		a.Tracef(TraceTagAttachOutput, "%s is rooted", wOut.IDShortString)
		return true
	}
	a.Tracef(TraceTagAttachOutput, "%s is NOT rooted", wOut.IDShortString)

	if wOut.Timestamp().Before(parasiticChainHorizon) {
		// parasitic chain rule
		err := fmt.Errorf("parasitic chain threshold %s has been broken while attaching output %s", parasiticChainHorizon.String(), wOut.IDShortString())
		a.setReason(err)
		a.Tracef(TraceTagAttachOutput, "%v", err)
		return false
	}

	// input is not rooted
	ok = true
	status := wOut.VID.GetTxStatus()
	wOut.VID.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			isSeq := wOut.VID.ID.IsSequencerMilestone()
			if !isSeq || status == vertex.Good {
				// go deeper if it is not seq milestone or its is good
				if isSeq {
					// good seq milestone, reset parasitic chain horizon
					parasiticChainHorizon = ledger.NilLogicalTime
				}
				ok = a.attachVertexUnwrapped(v, wOut.VID, parasiticChainHorizon) // >>>>>>> recursion
			} else {
				// not a seq milestone OR is not good yet -> don't go deeper
				a.pokeMe(wOut.VID)
			}
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			a.Pull(wOut.VID.ID)
			// ask environment to poke when transaction arrive
			a.pokeMe(wOut.VID)
		},
	})
	if !ok {
		return false
	}
	return true
}

func (a *attacher) branchesCompatible(vid1, vid2 *vertex.WrappedTx) bool {
	util.Assertf(vid1 != nil && vid2 != nil, "vid1 != nil && vid2 != nil")
	util.Assertf(vid1.IsBranchTransaction() && vid2.IsBranchTransaction(), "vid1.IsBranchTransaction() && vid2.IsBranchTransaction()")
	switch {
	case vid1 == vid2:
		return true
	case vid1.Slot() == vid2.Slot():
		// two different branches on the same slot conflicts
		return false
	case vid1.Slot() < vid2.Slot():
		return multistate.BranchIsDescendantOf(&vid2.ID, &vid1.ID, a.StateStore)
	default:
		return multistate.BranchIsDescendantOf(&vid1.ID, &vid2.ID, a.StateStore)
	}
}

// attachInputID links input vertex with the consumer, detects conflicts
func (a *attacher) attachInputID(consumerVertex *vertex.Vertex, consumerTx *vertex.WrappedTx, inputIdx byte) (ok bool) {
	inputOid := consumerVertex.Tx.MustInputAt(inputIdx)
	a.Tracef(TraceTagAttachOutput, "attachInputID: (oid = %s) #%d in %s", inputOid.StringShort, inputIdx, consumerTx.IDShortString)

	vidInputTx := consumerVertex.Inputs[inputIdx]
	if vidInputTx == nil {
		vidInputTx = AttachTxID(inputOid.TransactionID(), a, OptionInvokedBy(a.name))
	}
	util.Assertf(vidInputTx != nil, "vidInputTx != nil")

	if vidInputTx.GetTxStatus() == vertex.Bad {
		a.setReason(vidInputTx.GetReason())
		return false
	}

	if vidInputTx.IsSequencerMilestone() {
		// if input is a sequencer milestones, check if baselines are compatible
		if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
			if !a.branchesCompatible(a.baselineBranch, inputBaselineBranch) {
				err := fmt.Errorf("branches %s and %s not compatible", a.baselineBranch.IDShortString(), inputBaselineBranch.IDShortString())
				a.setReason(err)
				a.Tracef(TraceTagAttachOutput, "%v", err)
				return false
			}
		}
	}

	// attach consumer and check for conflicts
	// LEDGER CONFLICT (DOUBLE-SPEND) DETECTION
	util.Assertf(a.isKnownNotRooted(consumerTx), "attachInputID: a.isKnownNotRooted(consumerTx)")

	a.Tracef(TraceTagAttachOutput, "before AttachConsumer of %s:\n       good: %s\n       undef: %s",
		inputOid.StringShort,
		func() string { return vertex.VIDSetIDString(a.definedPastVertices) },
		func() string { return vertex.VIDSetIDString(a.undefinedPastVertices) },
	)

	if !vidInputTx.AttachConsumer(inputOid.Index(), consumerTx, a.checkConflictsFunc(consumerTx)) {
		err := fmt.Errorf("input %s of consumer %s conflicts with existing consumers in the baseline state %s (double spend)",
			inputOid.StringShort(), consumerTx.IDShortString(), a.baselineBranch.IDShortString())
		a.setReason(err)
		a.Tracef(TraceTagAttachOutput, "%v", err)
		return false
	}
	a.Tracef(TraceTagAttachOutput, "attached consumer %s of %s", consumerTx.IDShortString, inputOid.StringShort)

	consumerVertex.Inputs[inputIdx] = vidInputTx
	return true
}

func (a *attacher) checkConflictsFunc(consumerTx *vertex.WrappedTx) func(existingConsumers set.Set[*vertex.WrappedTx]) bool {
	return func(existingConsumers set.Set[*vertex.WrappedTx]) (conflict bool) {
		existingConsumers.ForEach(func(existingConsumer *vertex.WrappedTx) bool {
			if existingConsumer == consumerTx {
				return true
			}
			if a.definedPastVertices.Contains(existingConsumer) {
				conflict = true
				return false
			}
			if a.undefinedPastVertices.Contains(existingConsumer) {
				conflict = true
				return false
			}
			return true
		})
		return
	}
}

// setBaseline sets baseline, fetches its baselineCoverage and initializes attacher's baselineCoverage according to the currentTS
func (a *attacher) setBaseline(vid *vertex.WrappedTx, currentTS ledger.LogicalTime) {
	util.Assertf(vid.IsBranchTransaction(), "setBaseline: vid.IsBranchTransaction()")
	util.Assertf(currentTS.Slot() >= vid.Slot(), "currentTS.Slot() >= vid.Slot()")

	a.baselineBranch = vid
	if multistate.HistoryCoverageDeltas > 1 {
		rr, found := multistate.FetchRootRecord(a.StateStore(), a.baselineBranch.ID)
		util.Assertf(found, "setBaseline: can't fetch root record for %s", a.baselineBranch.IDShortString())

		a.coverage = rr.LedgerCoverage
	}
	util.Assertf(a.coverage.LatestDelta() == 0, "a.coverage.LatestDelta() == 0")
}

func (a *attacher) dumpLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("attacher %s", a.name)
	ret.Add("   baseline: %s", a.baselineBranch.String())
	ret.Add("   coverage: %s", a.coverage.String())
	ret.Add("   definedPastVertices:")
	a.definedPastVertices.ForEach(func(vid *vertex.WrappedTx) bool {
		ret.Add("        %s", vid.String())
		return true
	})
	ret.Add("   undefinedPastVertices:")
	a.undefinedPastVertices.ForEach(func(vid *vertex.WrappedTx) bool {
		ret.Add("        %s", vid.String())
		return true
	})
	ret.Add("   rooted:")
	for vid, consumed := range a.rooted {
		for idx := range consumed {
			o, err := vid.OutputAt(idx)
			if err == nil {
				oid := vid.OutputID(idx)
				ret.Add("         %s : %s", oid.StringShort(), util.GoTh(o.Amount()))
				ret.Append(o.Lines("                                 "))
			}
		}
	}
	return ret
}

func (a *attacher) allEndorsementsDefined(v *vertex.Vertex) (err error) {
	v.ForEachEndorsement(func(i byte, vidEndorsed *vertex.WrappedTx) bool {
		if !a.isKnownDefined(vidEndorsed) {
			err = fmt.Errorf("attacher %s: endorsement must be defined %s", a.name, vidEndorsed.String())
		}
		return err == nil
	})
	return
}

func (a *attacher) allInputsDefined(v *vertex.Vertex) (err error) {
	v.ForEachInputDependency(func(i byte, vidInput *vertex.WrappedTx) bool {
		if !a.isKnownDefined(vidInput) {
			err = fmt.Errorf("attacher %s: input must be defined %s", a.name, vidInput.String())
		}
		return err == nil
	})
	return
}
