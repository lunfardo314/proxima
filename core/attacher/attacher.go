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
	"golang.org/x/exp/maps"
)

func newPastConeAttacher(env Environment, name string) attacher {
	return attacher{
		Environment: env,
		name:        name,
		rooted:      make(map[*vertex.WrappedTx]set.Set[byte]),
		vertices:    make(map[*vertex.WrappedTx]Flags),
		referenced:  set.New[*vertex.WrappedTx](),
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

func (a *attacher) baselineStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(a.GetStateReaderForTheBranch(&a.baseline.ID))
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
	a.Assertf(flags.FlagsUp(FlagAttachedVertexKnown) && !flags.FlagsUp(FlagAttachedVertexDefined), "flags.FlagsUp(FlagKnown) && !flags.FlagsUp(FlagDefined)")
	a.vertices[vid] = flags | f
}

// markReferencedByAttacher maintains a set of vertices referenced by the attacher
func (a *attacher) markReferencedByAttacher(vid *vertex.WrappedTx) bool {
	if a.referenced.Contains(vid) {
		return true
	}
	if vid.Reference("markReferencedByAttacher" + a.Name()) {
		a.referenced.Insert(vid)
		return true
	}
	return false
}

func (a *attacher) mustMarkReferencedByAttacher(vid *vertex.WrappedTx) {
	a.Assertf(a.markReferencedByAttacher(vid), "attacher %s: failed to reference %s", a.name, vid.IDShortString)
}

// unReferenceAllByAttacher releases all references
func (a *attacher) unReferenceAllByAttacher() {
	for vid := range a.referenced {
		vid.UnReference("unReferenceAllByAttacher " + a.Name())
	}
	a.referenced = nil // invalidate
}

func (a *attacher) markVertexDefined(vid *vertex.WrappedTx) {
	a.mustMarkReferencedByAttacher(vid)
	a.vertices[vid] = a.flags(vid) | FlagAttachedVertexKnown | FlagAttachedVertexDefined

	a.Tracef(TraceTagMarkDefUndef, "markVertexDefined in %s: %s is DEFINED", a.name, vid.IDShortString)
}

func (a *attacher) markVertexUndefined(vid *vertex.WrappedTx) {
	a.mustMarkReferencedByAttacher(vid)
	f := a.flags(vid)
	a.Assertf(!f.FlagsUp(FlagAttachedVertexDefined), "!f.FlagsUp(FlagDefined)")
	a.vertices[vid] = f | FlagAttachedVertexKnown

	a.Tracef(TraceTagMarkDefUndef, "markVertexUndefined in %s: %s is UNDEFINED", a.name, vid.IDShortString)
}

func (a *attacher) markVertexRooted(vid *vertex.WrappedTx) {
	a.mustMarkReferencedByAttacher(vid)
	// creates entry in rooted, probably empty
	a.rooted[vid] = a.rooted[vid]
}

func (a *attacher) isKnown(vid *vertex.WrappedTx) bool {
	return a.flags(vid).FlagsUp(FlagAttachedVertexKnown)
}

func (a *attacher) isKnownDefined(vid *vertex.WrappedTx) bool {
	return a.flags(vid).FlagsUp(FlagAttachedVertexKnown | FlagAttachedVertexDefined)
}

func (a *attacher) isKnownUndefined(vid *vertex.WrappedTx) bool {
	f := a.flags(vid)
	if !f.FlagsUp(FlagAttachedVertexKnown) {
		return false
	}
	return !f.FlagsUp(FlagAttachedVertexDefined)
}

func (a *attacher) isKnownNotRooted(vid *vertex.WrappedTx) bool {
	known := a.isKnownDefined(vid) || a.isKnownUndefined(vid)
	_, rooted := a.rooted[vid]
	return known && !rooted
}

func (a *attacher) isKnownRooted(vid *vertex.WrappedTx) (yes bool) {
	_, yes = a.rooted[vid]
	a.Assertf(!yes || a.isKnownDefined(vid) || a.isKnownUndefined(vid), "!yes || a.isKnownDefined(vid) || a.isKnownUndefined(vid)")
	return
}

func (a *attacher) isRootedOutput(wOut vertex.WrappedOutput) bool {
	rootedIndices := a.rooted[wOut.VID]
	if len(rootedIndices) == 0 {
		return false
	}
	a.Assertf(!a.isKnownNotRooted(wOut.VID), "!a.isKnownNotRooted(wOut.VID)")
	return rootedIndices.Contains(wOut.Index)
}

// solidifyBaselineVertex directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *attacher) solidifyBaselineVertex(v *vertex.Vertex) (ok bool) {
	a.Assertf(a.baseline == nil, "a.baseline == nil")
	if v.Tx.IsBranchTransaction() {
		return a.solidifyStemOfTheVertex(v)
	}
	return a.solidifySequencerBaseline(v)
}

func (a *attacher) solidifyStemOfTheVertex(v *vertex.Vertex) (ok bool) {
	a.Assertf(v.BaselineBranch == nil, "v.BaselineBranchTxID == nil")

	stemInputIdx := v.StemInputIndex()
	stemInputOid := v.Tx.MustInputAt(stemInputIdx)
	stemVid := AttachTxID(stemInputOid.TransactionID(), a, OptionInvokedBy(a.name))
	if !a.markReferencedByAttacher(stemVid) {
		// failed to reference (pruned), but it is ok (rare event)
		return true
	}
	a.Assertf(stemVid.IsBranchTransaction(), "stemVid.IsBranchTransaction()")
	switch stemVid.GetTxStatus() {
	case vertex.Good:
		stemVid.Reference("solidifyStemOfTheVertex")
		v.BaselineBranch = stemVid
		return true
	case vertex.Bad:
		err := stemVid.GetError()
		a.AssertMustError(err)
		a.setError(err)
		return false
	case vertex.Undefined:
		a.pokeMe(stemVid)
		return true
	default:
		panic("wrong vertex state")
	}
}

func (a *attacher) solidifySequencerBaseline(v *vertex.Vertex) (ok bool) {
	// regular sequencer tx. Go to the direction of the baseline branch
	predOid, _ := v.Tx.SequencerChainPredecessor()
	a.Assertf(predOid != nil, "inconsistency: sequencer milestone cannot be a chain origin")
	var inputTx *vertex.WrappedTx

	// follow the endorsement if it is cross-slot or predecessor is not sequencer tx
	followTheEndorsement := predOid.Slot() != v.Tx.Slot() || !predOid.IsSequencerTransaction()
	if followTheEndorsement {
		// predecessor is on the earlier slot -> follow the first endorsement (guaranteed by the ledger constraint layer)
		a.Assertf(v.Tx.NumEndorsements() > 0, "v.Tx.NumEndorsements()>0")
		inputTx = AttachTxID(v.Tx.EndorsementAt(0), a, OptionPullNonBranch, OptionInvokedBy(a.name))
	} else {
		inputTx = AttachTxID(predOid.TransactionID(), a, OptionPullNonBranch, OptionInvokedBy(a.name))
	}
	if !a.markReferencedByAttacher(inputTx) {
		// wasn't able to reference but it is ok
		return true
	}
	switch inputTx.GetTxStatus() {
	case vertex.Good:
		v.BaselineBranch = inputTx.BaselineBranch()
		v.BaselineBranch.Reference("solidifySequencerBaseline")
		a.Assertf(v.BaselineBranch != nil, "v.BaselineBranchTxID!=nil")
		return true
	case vertex.Undefined:
		// vertex can be undefined but with correct baseline branch
		a.pokeMe(inputTx)
		return true
	case vertex.Bad:
		err := inputTx.GetError()
		a.AssertMustError(err)
		a.setError(err)
		return false
	default:
		panic("wrong vertex state")
	}
}

func (a *attacher) attachVertexNonBranch(vid *vertex.WrappedTx, parasiticChainHorizon ledger.Time) (ok, defined bool) {
	a.Assertf(!vid.IsBranchTransaction(), "!vid.IsBranchTransaction(): %s", vid.IDShortString)

	if a.isKnownDefined(vid) {
		return true, true
	}
	a.markVertexUndefined(vid)

	vid.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			switch vid.GetTxStatusNoLock() {
			case vertex.Undefined:
				if vid.IsSequencerMilestone() {
					// don't go deeper for undefined sequencers
					ok = true
					return
				}
				ok = a.attachVertexUnwrapped(v, vid, parasiticChainHorizon)
				if ok && vid.FlagsUpNoLock(vertex.FlagVertexConstraintsValid) {
					a.markVertexDefined(vid)
					defined = true
				}
			case vertex.Good:
				a.Assertf(vid.IsSequencerMilestone(), "vid.IsSequencerMilestone()")
				ok = a.attachVertexUnwrapped(v, vid, ledger.NilLedgerTime)
				if ok {
					a.markVertexDefined(vid)
					defined = true
				}
			case vertex.Bad:
			default:
				a.Log().Fatalf("inconsistency: wrong tx status")
			}
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			ok = true
			a.Pull(vid.ID)
		},
	})
	if !defined {
		a.pokeMe(vid)
	}
	a.Assertf(ok || a.err != nil, "ok || a.err != nil")
	return
}

// attachVertexUnwrapped: vid corresponds to the vertex v
// it solidifies vertex by traversing the past cone down to rooted outputs or undefined vertices
// Repetitive calling of the function reaches all past vertices down to the rooted outputs
// The exit condition of the loop is fully determined states of the past cone.
// It results in all vertices are vertex.Good
// Otherwise, repetition reaches conflict (double spend) or vertex.Bad vertex and exits
// Returns OK (= not bad)
func (a *attacher) attachVertexUnwrapped(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.Time) (ok bool) {
	a.Assertf(!v.Tx.IsSequencerMilestone() || a.baseline != nil, "!v.Tx.IsSequencerMilestone() || a.baseline != nil in %s", v.Tx.IDShortString)

	if vid.GetTxStatusNoLock() == vertex.Bad {
		a.setError(vid.GetErrorNoLock())
		a.Assertf(a.err != nil, "a.err != nil")
		return false
	}

	a.Tracef(TraceTagAttachVertex, " %s IN: %s", a.name, vid.IDShortString)
	a.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")

	if !a.flags(vid).FlagsUp(FlagAttachedVertexEndorsementsSolid) {
		a.Tracef(TraceTagAttachVertex, "attacher %s: endorsements not solid in %s", a.name, v.Tx.IDShortString())
		// depth-first along endorsements
		if !a.attachEndorsements(v, vid) { // <<< recursive
			// not ok -> abandon attacher
			a.Assertf(a.err != nil, "a.err != nil")
			return false
		}
	}
	// check consistency
	if a.flags(vid).FlagsUp(FlagAttachedVertexEndorsementsSolid) {
		err := a.allEndorsementsDefined(v)
		a.Assertf(err == nil, "%w:\nvertices: %s", err, a.linesVertices("       ").String)

		a.Tracef(TraceTagAttachVertex, "attacher %s: endorsements (%d) are all solid in %s", a.name, v.Tx.NumEndorsements(), v.Tx.IDShortString)
	} else {
		a.Tracef(TraceTagAttachVertex, "attacher %s: endorsements (%d) NOT solid in %s", a.name, v.Tx.NumEndorsements(), v.Tx.IDShortString)
	}

	inputsOk := a.attachInputsOfTheVertex(v, vid, parasiticChainHorizon) // deep recursion
	if !inputsOk {
		a.AssertMustError(a.err)
		return false
	}

	if !v.Tx.IsSequencerMilestone() && a.flags(vid).FlagsUp(FlagAttachedVertexInputsSolid) {
		if !a.finalTouchNonSequencer(v, vid) {
			a.AssertMustError(a.err)
			return false
		}
	}
	a.Tracef(TraceTagAttachVertex, "attacher %s: return OK: %s", a.name, v.Tx.IDShortString)
	return true
}

func (a *attacher) finalTouchNonSequencer(v *vertex.Vertex, vid *vertex.WrappedTx) (ok bool) {
	glbFlags := vid.FlagsNoLock()
	if !glbFlags.FlagsUp(vertex.FlagVertexConstraintsValid) {
		// constraints are not validated yet
		if err := v.ValidateConstraints(); err != nil {
			v.UnReferenceDependencies()
			a.setError(err)
			a.Tracef(TraceTagAttachVertex, "constraint validation failed in %s: '%v'", vid.IDShortString(), err)
			return false
		}
		vid.SetFlagsUpNoLock(vertex.FlagVertexConstraintsValid)

		a.Tracef(TraceTagAttachVertex, "constraints has been validated OK: %s", v.Tx.IDShortString)
		a.PokeAllWith(vid)
	}
	glbFlags = vid.FlagsNoLock()
	a.Assertf(glbFlags.FlagsUp(vertex.FlagVertexConstraintsValid), "glbFlags.FlagsUp(vertex.FlagConstraintsValid)")

	// persist bytes of the valid non-sequencer transaction, if not yet persisted
	// non-sequencer transaction always have empty persistent metadata
	// (sequencer transactions will be persisted upon finalization of the attacher)
	if !glbFlags.FlagsUp(vertex.FlagVertexTxBytesPersisted) {
		a.AsyncPersistTxBytesWithMetadata(v.Tx.Bytes(), nil)
		vid.SetFlagsUpNoLock(vertex.FlagVertexTxBytesPersisted)

		a.Tracef(TraceTagAttachVertex, "tx bytes persisted: %s", v.Tx.IDShortString)
	}
	// non-sequencer, all inputs solid, constraints valid -> we can mark it 'defined' in the attacher
	a.markVertexDefined(vid)
	return true
}

func (a *attacher) validateSequencerTx(v *vertex.Vertex, vid *vertex.WrappedTx) (ok, finalSuccess bool) {
	flags := a.flags(vid)
	if !flags.FlagsUp(FlagAttachedVertexEndorsementsSolid) || !flags.FlagsUp(FlagAttachedVertexInputsSolid) {
		return true, false
	}
	// inputs solid
	glbFlags := vid.FlagsNoLock()
	a.Assertf(!glbFlags.FlagsUp(vertex.FlagVertexConstraintsValid), "%s: !glbFlags.FlagsUp(vertex.FlagConstraintsValid) in %s", a.name, vid.IDShortString)

	if err := v.ValidateConstraints(); err != nil {
		a.setError(err)
		a.Tracef(TraceTagAttachVertex, "constraint validation failed in %s: '%v'", vid.IDShortString, err)
		return false, false
	}
	vid.SetFlagsUpNoLock(vertex.FlagVertexConstraintsValid)
	a.Tracef(TraceTagAttachVertex, "constraints has been validated OK: %s", v.Tx.IDShortString)
	return true, true
}

const TraceTagAttachEndorsements = "attachEndorsements"

// Attaches endorsements of the vertex
// Return OK (== not bad)
func (a *attacher) attachEndorsements(v *vertex.Vertex, vid *vertex.WrappedTx) bool {
	a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s) IN of %s", a.name, v.Tx.IDShortString)
	defer a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s) OUT of %s return", a.name, v.Tx.IDShortString)

	a.Assertf(!a.flags(vid).FlagsUp(FlagAttachedVertexEndorsementsSolid), "!v.FlagsUp(vertex.FlagAttachedvertexEndorsementsSolid)")

	numUndefined := len(v.Endorsements)
	for i, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			vidEndorsed = AttachTxID(v.Tx.EndorsementAt(byte(i)), a, OptionPullNonBranch, OptionInvokedBy(a.name))
			if !v.ReferenceEndorsement(byte(i), vidEndorsed) {
				// if failed to reference, remains nil
				a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): failed to reference endorsement %s", a.name, vidEndorsed.IDShortString)
				continue
			}
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
		if !a.branchesCompatible(&a.baseline.ID, &baselineBranch.ID) {
			a.setError(fmt.Errorf("attachEndorsements: baseline %s of endorsement %s is incompatible with the baseline branch %s",
				baselineBranch.IDShortString(), vidEndorsed.IDShortString(), a.baseline.IDShortString()))
			a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): not compatible with baselines", a.name)
			return false
		}

		switch vidEndorsed.GetTxStatus() {
		case vertex.Bad:
			a.Assertf(!a.isKnownDefined(vidEndorsed), "attachEndorsements: !a.isKnownDefined(vidEndorsed)")
			a.setError(vidEndorsed.GetError())
			a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): %s is BAD", a.name, vidEndorsed.IDShortString)
			return false
		case vertex.Good:
			if a.baselineStateReader().KnowsCommittedTransaction(&vidEndorsed.ID) {
				// all endorsed transactions known to the baseline state are 'defined' and 'rooted'
				a.markVertexRooted(vidEndorsed)
				a.markVertexDefined(vidEndorsed)
				numUndefined--
				continue
			}
		}
		// non-branch undefined milestone. Go deep recursively
		a.Assertf(!vidEndorsed.IsBranchTransaction(), "attachEndorsements(%s): !vidEndorsed.IsBranchTransaction(): %s", a.name, vidEndorsed.IDShortString)

		ok, defined := a.attachVertexNonBranch(vidEndorsed, ledger.NilLedgerTime)
		if !ok {
			a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): attachVertexNonBranch returned: endorsement %s -> %s NOT OK",
				a.name, vid.IDShortString, vidEndorsed.IDShortString)
			a.AssertMustError(a.err)
			return false
		}
		a.AssertNoError(a.err)
		if defined {
			numUndefined--
		}
	}
	if numUndefined == 0 {
		a.AssertNoError(a.allEndorsementsDefined(v))
		a.setFlagsUp(vid, FlagAttachedVertexEndorsementsSolid)
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): endorsements are all good in %s", a.name, v.Tx.IDShortString)
	} else {
		a.Tracef(TraceTagAttachEndorsements, "attachEndorsements(%s): endorsements are NOT all good in %s", a.name, v.Tx.IDShortString)
	}
	return true
}

func (a *attacher) attachInputsOfTheVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.Time) (ok bool) {
	a.Tracef(TraceTagAttachVertex, "attachInputsOfTheVertex in %s", vid.IDShortString)
	numUndefined := v.Tx.NumInputs()
	notSolid := make([]byte, 0, v.Tx.NumInputs())
	var success bool
	for i := range v.Inputs {
		ok, success = a.attachInput(v, byte(i), vid, parasiticChainHorizon)
		if !ok {
			a.Assertf(a.err != nil, "a.err != nil")
			return false
		}
		if success {
			numUndefined--
		} else {
			notSolid = append(notSolid, byte(i))
		}
	}
	if numUndefined == 0 {
		a.setFlagsUp(vid, FlagAttachedVertexInputsSolid)
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

func (a *attacher) attachInput(v *vertex.Vertex, inputIdx byte, vid *vertex.WrappedTx, parasiticChainHorizon ledger.Time) (ok, defined bool) {
	a.Tracef(TraceTagAttachVertex, "attachInput #%d of %s", inputIdx, vid.IDShortString)
	if !a.attachInputID(v, vid, inputIdx) {
		a.Tracef(TraceTagAttachVertex, "bad input %d", inputIdx)
		return false, false
	}
	a.Assertf(v.Inputs[inputIdx] != nil, "v.Inputs[i] != nil")

	if parasiticChainHorizon == ledger.NilLedgerTime {
		// TODO revisit parasitic chain threshold because of syncing branches
		parasiticChainHorizon = v.Inputs[inputIdx].Timestamp().SubtractSlotsSafe(ledger.Slot(ledger.L().ID.MaxToleratedParasiticChainSlots))
	}
	wOut := vertex.WrappedOutput{
		VID:   v.Inputs[inputIdx],
		Index: v.Tx.MustOutputIndexOfTheInput(inputIdx),
	}

	ok, defined = a.attachOutput(wOut, parasiticChainHorizon)
	if !ok {
		return ok, false
	}
	defined = a.isKnownDefined(v.Inputs[inputIdx]) || a.isRootedOutput(wOut)
	if defined {
		a.Tracef(TraceTagAttachVertex, "attacher %s: input #%d (%s) has been solidified", a.name, inputIdx, wOut.IDShortString)
	}
	return true, defined
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
	a.Assertf(ensured, "ensureOutput: internal inconsistency")

	if len(consumedRooted) == 0 {
		consumedRooted = set.New[byte](wOut.Index)
	} else {
		consumedRooted.Insert(wOut.Index)
	}

	a.markVertexRooted(wOut.VID)
	a.rooted[wOut.VID] = consumedRooted
	a.markVertexDefined(wOut.VID)

	// this is new rooted output -> add to the coverage delta
	a.coverage.AddDelta(out.Amount())
	return true, true
}

func (a *attacher) attachOutput(wOut vertex.WrappedOutput, parasiticChainHorizon ledger.Time) (ok, defined bool) {
	a.Tracef(TraceTagAttachOutput, "%s", wOut.IDShortString)

	ok, isRooted := a.attachRooted(wOut)
	if !ok {
		return false, false
	}
	if isRooted {
		a.Tracef(TraceTagAttachOutput, "%s is rooted", wOut.IDShortString)
		return true, true
	}
	// not rooted
	a.Tracef(TraceTagAttachOutput, "%s is NOT rooted", wOut.IDShortString)

	if wOut.VID.IsBranchTransaction() {
		// not rooted branch output -> BAD
		err := fmt.Errorf("attachOutput: branch output %s is expected to be rooted in the baseline %s", wOut.VID.IDShortString(), a.baseline.IDShortString())
		a.setError(err)
		a.Tracef(TraceTagAttachOutput, "%v", err)
		return false, false
	}

	if wOut.Timestamp().Before(parasiticChainHorizon) {
		// parasitic chain rule
		err := fmt.Errorf("attachOutput: parasitic chain threshold %s has been broken while attaching output %s", parasiticChainHorizon.String(), wOut.IDShortString())
		a.setError(err)
		a.Tracef(TraceTagAttachOutput, "%v", err)
		return false, false
	}

	a.Assertf(!wOut.VID.IsBranchTransaction(), "attachOutput: !wOut.VID.IsBranchTransaction(): %s", wOut.IDShortString)

	// input is not rooted
	// check if output index is correct??
	return a.attachVertexNonBranch(wOut.VID, parasiticChainHorizon)
}

func (a *attacher) branchesCompatible(txid1, txid2 *ledger.TransactionID) bool {
	a.Assertf(txid1 != nil && txid2 != nil, "txid1 != nil && txid2 != nil")
	a.Assertf(txid1.IsBranchTransaction() && txid2.IsBranchTransaction(), "txid1.IsBranchTransaction() && txid2.IsBranchTransaction()")
	switch {
	case *txid1 == *txid2:
		return true
	case txid1.Slot() == txid2.Slot():
		// two different branches on the same slot conflicts
		return false
	case txid1.Slot() < txid2.Slot():
		return multistate.BranchIsDescendantOf(txid2, txid1, a.StateStore)
	default:
		return multistate.BranchIsDescendantOf(txid1, txid2, a.StateStore)
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
	a.Assertf(vidInputTx != nil, "vidInputTx != nil")

	if vidInputTx.GetTxStatus() == vertex.Bad {
		a.setError(vidInputTx.GetError())
		return false
	}

	if vidInputTx.IsSequencerMilestone() {
		// if input is a sequencer milestones, check if baselines are compatible
		if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
			if !a.branchesCompatible(&a.baseline.ID, &inputBaselineBranch.ID) {
				err := fmt.Errorf("branches %s and %s not compatible", a.baseline.IDShortString(), inputBaselineBranch.IDShortString())
				a.setError(err)
				a.Tracef(TraceTagAttachOutput, "%v", err)
				return false
			}
		}
	}

	// attach consumer and check for conflicts
	// LEDGER CONFLICT (DOUBLE-SPEND) DETECTION
	a.Assertf(a.isKnownNotRooted(consumerTx), "attachInputID: a.isKnownNotRooted(consumerTx)")

	if !vidInputTx.AttachConsumer(inputOid.Index(), consumerTx, a.checkConflictsFunc(consumerTx)) {
		err := fmt.Errorf("input %s of consumer %s conflicts with existing consumers in the baseline state %s (double spend)",
			inputOid.StringShort(), consumerTx.IDShortString(), a.baseline.IDShortString())
		a.setError(err)
		a.Tracef(TraceTagAttachOutput, "%v", err)
		return false
	}
	a.Tracef(TraceTagAttachOutput, "attached consumer %s of %s", consumerTx.IDShortString, inputOid.StringShort)

	// only will become solid if successfully referenced
	if consumerVertex.Inputs[inputIdx] == nil {
		consumerVertex.ReferenceInput(inputIdx, vidInputTx)
	}
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

func (a *attacher) isKnownConsumed(wOut vertex.WrappedOutput) (isConsumed bool) {
	wOut.VID.ConsumersOf(wOut.Index).ForEach(func(consumer *vertex.WrappedTx) bool {
		isConsumed = a.isKnown(consumer)
		return !isConsumed
	})
	return
}

// setBaseline sets baseline, fetches its baselineCoverage and initializes attacher's baselineCoverage according to the currentTS
func (a *attacher) setBaseline(vid *vertex.WrappedTx, currentTS ledger.Time) bool {
	a.Assertf(vid.IsBranchTransaction(), "setBaseline: vid.IsBranchTransaction()")
	a.Assertf(currentTS.Slot() >= vid.Slot(), "currentTS.Slot() >= vid.Slot()")

	if !a.markReferencedByAttacher(vid) {
		return false
	}
	a.baseline = vid

	rr, found := multistate.FetchRootRecord(a.StateStore(), a.baseline.ID)
	a.Assertf(found, "setBaseline: can't fetch root record for %s", a.baseline.IDShortString)

	// shifting baseline coverage by 1 or more slots. Current delta becomes 0
	var shiftCoverageBy int
	if currentTS.Tick() == 0 {
		shiftCoverageBy = int(currentTS.Slot() - vid.Slot())
	} else {
		shiftCoverageBy = int(currentTS.Slot()-vid.Slot()) + 1
	}
	a.coverage = rr.LedgerCoverage.Shift(shiftCoverageBy)
	a.Assertf(a.coverage.LatestDelta() == 0, "a.coverage.LatestDelta() == 0")
	a.baselineSupply = rr.Supply
	return true
}

func (a *attacher) dumpLines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("attacher %s", a.name)
	ret.Add("   baseline: %s", a.baseline.String())
	ret.Add("   coverage: %s", a.coverage.String())
	ret.Add("   baselineSupply: %s", util.GoTh(a.baselineSupply))
	ret.Add("   vertices:")
	ret.Append(a.linesVertices(prefix...))
	ret.Add("   rooted:")
	for vid, consumed := range a.rooted {
		for idx := range consumed {
			o, err := vid.OutputAt(idx)
			consumers := vid.ConsumersOf(idx)
			consStr := vertex.VerticesIDLines(maps.Keys(consumers)).Join(", ")
			if err == nil {
				oid := vid.OutputID(idx)
				ret.Add("         %s : amount: %s, consumers: {%s}", oid.StringShort(), util.GoTh(o.Amount()), consStr)
				ret.Append(o.Lines("                    "))
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
			err = fmt.Errorf("attacher %s: input #%d must be 'defined' in the tx:\n   %s\nvertices:\n%s",
				a.name, i, vidInput.String(), a.linesVertices("   "))
		}
		return err == nil
	})
	return
}

func (a *attacher) containsUndefinedExcept(except *vertex.WrappedTx) bool {
	for vid, flags := range a.vertices {
		if !flags.FlagsUp(FlagAttachedVertexDefined) && vid != except {
			return true
		}
	}
	return false
}

func (a *attacher) calculateSlotInflation() {
	a.slotInflation = 0
	for vid := range a.vertices {
		if _, isRooted := a.rooted[vid]; !isRooted {
			a.slotInflation += vid.InflationAmount()
		}
	}
}

func (a *attacher) Tracef(traceLabel string, format string, args ...any) {
	a.Environment.Tracef(traceLabel, a.name+format+" ", args...)
}

func (a *attacher) SlotInflation() uint64 {
	return a.slotInflation
}
