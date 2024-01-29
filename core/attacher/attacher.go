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
		Environment: env,
		name:        name,
		rooted:      make(map[*vertex.WrappedTx]set.Set[byte]),
		vertices:    make(map[*vertex.WrappedTx]Flags),
		pokeMe:      func(_ *vertex.WrappedTx) {},
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
	return a.err
}

func (a *attacher) baselineStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(a.GetStateReaderForTheBranch(a.baseline))
}

func (a *attacher) setError(err error) {
	a.Tracef(TraceTagAttach, "set err: '%v'", err)
	a.err = err
}

const TraceTagMarkDefUndef = "markDefUndef"

func (a *attacher) flags(vid *vertex.WrappedTx) Flags {
	return a.vertices[vid]
}

func (a *attacher) setFlagsUp(vid *vertex.WrappedTx, f Flags) {
	flags := a.flags(vid)
	util.Assertf(flags.FlagsUp(FlagKnown) && !flags.FlagsUp(FlagDefined), "flags.FlagsUp(FlagKnown) && !flags.FlagsUp(FlagDefined)")
	a.vertices[vid] = flags | f
}

func (a *attacher) markVertexDefined(vid *vertex.WrappedTx) {
	util.Assertf(a.isKnownUndefined(vid), "a.isKnownUndefined(vid)")
	a.vertices[vid] = a.flags(vid) | FlagDefined

	a.Tracef(TraceTagMarkDefUndef, "markVertexDefined in %s: %s is DEFINED", a.name, vid.IDShortString)
}

func (a *attacher) markVertexUndefined(vid *vertex.WrappedTx) {
	f := a.flags(vid)
	util.Assertf(!f.FlagsUp(FlagDefined), "!f.FlagsUp(FlagDefined)")
	a.vertices[vid] = f | FlagKnown

	a.Tracef(TraceTagMarkDefUndef, "markVertexUndefined in %s: %s is UNDEFINED", a.name, vid.IDShortString)
}

func (a *attacher) isKnownDefined(vid *vertex.WrappedTx) bool {
	return a.flags(vid).FlagsUp(FlagKnown | FlagDefined)
}

func (a *attacher) isKnownUndefined(vid *vertex.WrappedTx) bool {
	f := a.flags(vid)
	if !f.FlagsUp(FlagKnown) {
		return false
	}
	return !f.FlagsUp(FlagDefined)
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
	util.Assertf(v.Inputs[stemInputIdx].IsBranchTransaction(), "v.Inputs[stemInputIdx].IsBranchTransaction()")

	status := v.Inputs[stemInputIdx].GetTxStatus()
	switch status {
	case vertex.Good:
		//v.BaselineBranch = v.Inputs[stemInputIdx].BaselineBranch()
		v.BaselineBranch = v.Inputs[stemInputIdx]
		return true
	case vertex.Bad:
		a.setError(v.Inputs[stemInputIdx].GetReason())
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
	predOid, _ := v.Tx.SequencerChainPredecessor()
	util.Assertf(predOid != nil, "inconsistency: sequencer milestone cannot be a chain origin")
	var inputTx *vertex.WrappedTx

	// follow the endorsement if it is cross-slot or predecessor is not sequencer tx
	followTheEndorsement := predOid.TimeSlot() != v.Tx.TimeSlot() || !predOid.IsSequencerTransaction()
	if followTheEndorsement {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		util.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		inputTx = AttachTxID(v.Tx.EndorsementAt(0), a, OptionPullNonBranch, OptionInvokedBy(a.name))
	} else {
		inputTx = AttachTxID(predOid.TransactionID(), a, OptionPullNonBranch, OptionInvokedBy(a.name))
	}
	switch inputTx.GetTxStatus() {
	case vertex.Good:
		v.BaselineBranch = inputTx.BaselineBranch()
		util.Assertf(v.BaselineBranch != nil, "v.BaselineBranch!=nil")
		return true
	case vertex.Undefined:
		// vertex can be undefined but with correct baseline branch
		a.pokeMe(inputTx)
		return true
	case vertex.Bad:
		a.setError(inputTx.GetReason())
		return false
	default:
		panic("wrong vertex state")
	}
}

// attachVertexUnwrapped: vid corresponds to the vertex v
// it solidifies vertex by traversing the past cone down to rooted tagAlongInputs or undefined vertices
// Repetitive calling of the function reaches all past vertices down to the rooted tagAlongInputs in the vertices set, while
// the undefinedPastVertices set becomes empty This is the exit condition of the loop.
// It results in all vertices are vertex.Good
// Otherwise, repetition reaches conflict (double spend) or vertex.Bad vertex and exits
// Returns OK (= not bad)
func (a *attacher) attachVertexUnwrapped(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime) (ok bool) {
	util.Assertf(!v.Tx.IsSequencerMilestone() || a.baseline != nil, "!v.Tx.IsSequencerMilestone() || a.baseline != nil in %s", v.Tx.IDShortString)

	if vid.GetTxStatusNoLock() == vertex.Bad {
		a.setError(vid.GetErrorNoLock())
		return false
	}

	a.Tracef(TraceTagAttachVertex, " %s IN: %s", a.name, vid.IDShortString)
	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")

	if a.isKnownDefined(vid) {
		// it is done with the vertex, it's state in the attacher is fully determined and immutable from now on
		return true
	}
	// mark vertex in the past cone undefined. If it is already known, it won't change its flags
	a.markVertexUndefined(vid)

	if !a.flags(vid).FlagsUp(FlagEndorsementsSolid) {
		a.Tracef(TraceTagAttachVertex, "attacher %s: endorsements not solid in %s", a.name, v.Tx.IDShortString())
		// depth-first along endorsements
		if !a.attachEndorsements(v, vid, parasiticChainHorizon) { // <<< recursive
			// not ok -> abandon attacher
			return false
		}
	}
	// check consistency
	if a.flags(vid).FlagsUp(FlagEndorsementsSolid) {
		err := a.allEndorsementsDefined(v)
		util.Assertf(err == nil, "%w:\nvertices: %s", err, a.linesVertices("       ").String)

		a.Tracef(TraceTagAttachVertex, "attacher %s: endorsements (%d) are all solid in %s", a.name, v.Tx.NumEndorsements(), v.Tx.IDShortString)
	} else {
		a.Tracef(TraceTagAttachVertex, "attacher %s: endorsements (%d) NOT solid in %s", a.name, v.Tx.NumEndorsements(), v.Tx.IDShortString)
	}

	inputsOk := a.attachInputsOfTheVertex(v, vid, parasiticChainHorizon) // deep recursion
	if !inputsOk {
		return false
	}

	if a.flags(vid).FlagsUp(FlagInputsSolid) {
		// TODO nice-to-have optimization: constraints can be validated even before the vertex becomes good (solidified).
		//  It is enough to have all tagAlongInputs available, i.e. before full solidification of the past cone

		glbFlags := vid.Flags()
		if !glbFlags.FlagsUp(vertex.FlagConstraintsValid) {
			if err := v.ValidateConstraints(); err != nil {
				a.setError(err)
				vid.SetTxStatusBadNoLock(err)
				a.Tracef(TraceTagAttachVertex, "constraint validation failed in %s: '%v'", vid.IDShortString(), err)
				return false
			}
			vid.SetFlagOnNoLock(vertex.FlagConstraintsValid)
			a.Tracef(TraceTagAttachVertex, "constraints has been validated OK: %s", v.Tx.IDShortString)
		}

		if !v.Tx.IsSequencerMilestone() && !glbFlags.FlagsUp(vertex.FlagTxBytesPersisted) {
			// persist bytes of the valid non-sequencer transaction, if needed
			// non-sequencer transaction always have empty persistent metadata
			// sequencer transaction will be persisted upon finalization of the attacher
			a.AsyncPersistTxBytesWithMetadata(v.Tx.Bytes(), nil)
			vid.SetFlagOnNoLock(vertex.FlagTxBytesPersisted)
			a.Tracef(TraceTagAttachVertex, "tx bytes persisted: %s", v.Tx.IDShortString)
		}
	}
	if a.flags(vid).FlagsUp(FlagEndorsementsSolid | FlagInputsSolid) {
		a.markVertexDefined(vid)
	}
	a.Tracef(TraceTagAttachVertex, "attacher %s: return OK: %s", a.name, v.Tx.IDShortString)
	return true
}

const TraceTagAttachEndorsements = "attachEndorsements"

// Attaches endorsements of the vertex
// Return OK (== not bad)
func (a *attacher) attachEndorsements(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime) bool {
	a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s) IN of %s", a.name, v.Tx.IDShortString)
	defer a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s) OUT of %s return", a.name, v.Tx.IDShortString)

	util.Assertf(!a.flags(vid).FlagsUp(FlagEndorsementsSolid), "!v.FlagsUp(vertex.FlagEndorsementsSolid)")

	numUndefined := len(v.Endorsements)
	for i, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			vidEndorsed = AttachTxID(v.Tx.EndorsementAt(byte(i)), a, OptionPullNonBranch, OptionInvokedBy(a.name))
			v.Endorsements[i] = vidEndorsed
		}
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): endorsement %s", a.name, vidEndorsed.IDShortString)

		if a.isKnownDefined(vidEndorsed) {
			a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): is known 'defined' %s", a.name, vidEndorsed.IDShortString)
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
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): baseline branch %s", a.name, baselineBranch.IDShortString)

		// baseline must be compatible with baseline of the attacher
		if !a.branchesCompatible(a.baseline, baselineBranch) {
			a.setError(fmt.Errorf("attachEndorsements: baseline %s of endorsement %s is incompatible with the baseline branch%s",
				baselineBranch.IDShortString(), vidEndorsed.IDShortString(), a.baseline.IDShortString()))
			a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): not compatible with baselines", a.name)
			return false
		}
		// sanity check: only one branch transaction can be endorsed
		util.Assertf(!vidEndorsed.IsBranchTransaction() || baselineBranch == a.baseline,
			"attachEndorsements: !vidEndorsed.IsBranchTransaction() || baseline == a.baseline")

		switch vidEndorsed.GetTxStatus() {
		case vertex.Bad:
			util.Assertf(!a.isKnownDefined(vidEndorsed), "attachEndorsements: !a.isKnownDefined(vidEndorsed)")
			a.setError(vidEndorsed.GetReason())
			a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): %s is BAD", a.name, vidEndorsed.IDShortString)
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
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): non-branch before unwrap %s", a.name, vidEndorsed.String)
		ok := true
		vidEndorsed.Unwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				ok = a.attachVertexUnwrapped(v, vidEndorsed, parasiticChainHorizon) // <<<<<<<<<<< recursion
			},
			VirtualTx: func(v *vertex.VirtualTransaction) {
				fmt.Printf(">>>>>>>>>>>>>>>\n")
			},
		})
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): non-branch after unwrap %s. ok = %v", a.name, vidEndorsed.String, ok)
		if !ok {
			a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): %s attachVertex not ok", a.name, vidEndorsed.IDShortString)
			a.setError(vidEndorsed.GetReason())
			return false
		}
		util.AssertNoError(a.err)

		// check status again
		switch status := vidEndorsed.GetTxStatus(); status {
		case vertex.Good:
			// this endorsement is defined for the attacher
			a.markVertexDefined(vidEndorsed)
			numUndefined--
		case vertex.Undefined:
			a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): %s is undef", a.name, vidEndorsed.IDShortString)
			a.pokeMe(vidEndorsed)
		default:
			a.Log().Fatalf("attacher %s: unexpected state of the vertex %s (%s)", a.name, vidEndorsed.IDShortString(), status.String())
		}
	}
	if numUndefined == 0 {
		util.AssertNoError(a.allEndorsementsDefined(v))
		a.setFlagsUp(vid, FlagEndorsementsSolid)
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): endorsements are all good in %s", a.name, v.Tx.IDShortString)
	} else {
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): endorsements are NOT all good in %s", a.name, v.Tx.IDShortString)
	}
	return true
}

func (a *attacher) attachInputsOfTheVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime) (ok bool) {
	a.Tracef(TraceTagAttachVertex, "attachInputsOfTheVertex in %s", vid.IDShortString)
	numUndefined := v.Tx.NumInputs()
	notSolid := make([]byte, 0, v.Tx.NumInputs())
	var success bool
	for i := range v.Inputs {
		ok, success = a.attachInput(v, byte(i), vid, parasiticChainHorizon)
		if !ok {
			return false
		}
		if !success {
			notSolid = append(notSolid, byte(i))
		} else {
			numUndefined--
		}
	}
	if numUndefined == 0 {
		a.setFlagsUp(vid, FlagInputsSolid)
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
		a.Tracef(TraceTagAttachVertex, "attacher %s: input #%d (%s) solidified", a.name, inputIdx, wOut.IDShortString)
	}
	return true, success
}

func (a *attacher) attachRooted(wOut vertex.WrappedOutput) (ok bool, isRooted bool) {
	a.Tracef(TraceTagAttachOutput, "attachRooted %s", wOut.IDShortString)
	if wOut.Timestamp().After(a.baseline.Timestamp()) {
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
		err := fmt.Errorf("output %s is already consumed in the baseline state %s", wOut.IDShortString(), a.baseline.IDShortString())
		a.setError(err)
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
		a.setError(err)
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
		a.setError(vidInputTx.GetReason())
		return false
	}

	if vidInputTx.IsSequencerMilestone() {
		// if input is a sequencer milestones, check if baselines are compatible
		if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
			if !a.branchesCompatible(a.baseline, inputBaselineBranch) {
				err := fmt.Errorf("branches %s and %s not compatible", a.baseline.IDShortString(), inputBaselineBranch.IDShortString())
				a.setError(err)
				a.Tracef(TraceTagAttachOutput, "%v", err)
				return false
			}
		}
	}

	// attach consumer and check for conflicts
	// LEDGER CONFLICT (DOUBLE-SPEND) DETECTION
	util.Assertf(a.isKnownNotRooted(consumerTx), "attachInputID: a.isKnownNotRooted(consumerTx)")

	//a.Tracef(TraceTagAttachOutput, "before AttachConsumer of %s:\n       good: %s\n       undef: %s",
	//	inputOid.StringShort,
	//	func() string { return vertex.VIDSetIDString(a.vertices) },
	//	func() string { return vertex.VIDSetIDString(a.undefinedPastVertices) },
	//)

	if !vidInputTx.AttachConsumer(inputOid.Index(), consumerTx, a.checkConflictsFunc(consumerTx)) {
		err := fmt.Errorf("input %s of consumer %s conflicts with existing consumers in the baseline state %s (double spend)",
			inputOid.StringShort(), consumerTx.IDShortString(), a.baseline.IDShortString())
		a.setError(err)
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
			if _, found := a.vertices[existingConsumer]; found {
				conflict = true
			}
			return !conflict
		})
		return
	}
}

// setBaseline sets baseline, fetches its baselineCoverage and initializes attacher's baselineCoverage according to the currentTS
func (a *attacher) setBaseline(vid *vertex.WrappedTx, currentTS ledger.LogicalTime) {
	util.Assertf(vid.IsBranchTransaction(), "setBaseline: vid.IsBranchTransaction()")
	util.Assertf(currentTS.Slot() >= vid.Slot(), "currentTS.Slot() >= vid.Slot()")

	a.baseline = vid
	if multistate.HistoryCoverageDeltas > 1 {
		rr, found := multistate.FetchRootRecord(a.StateStore(), a.baseline.ID)
		util.Assertf(found, "setBaseline: can't fetch root record for %s", a.baseline.IDShortString())

		a.coverage = rr.LedgerCoverage
	}
	util.Assertf(a.coverage.LatestDelta() == 0, "a.coverage.LatestDelta() == 0")
}

func (a *attacher) dumpLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("attacher %s", a.name)
	ret.Add("   baseline: %s", a.baseline.String())
	ret.Add("   coverage: %s", a.coverage.String())
	ret.Add("   vertices:")
	ret.Append(a.linesVertices(prefix...))
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

func (a *attacher) linesVertices(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for vid, flags := range a.vertices {
		ret.Add("%s local: %08b", vid.IDShortString(), flags)
	}
	return ret
}

func (a *attacher) allEndorsementsDefined(v *vertex.Vertex) (err error) {
	v.ForEachEndorsement(func(i byte, vidEndorsed *vertex.WrappedTx) bool {
		if !a.isKnownDefined(vidEndorsed) {
			err = fmt.Errorf("attacher %s: endorsement by %s must be 'defined' %s", a.name, v.Tx.IDShortString(), vidEndorsed.String())
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
