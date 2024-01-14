package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

type (
	DAGAccessEnvironment interface {
		WithGlobalWriteLock(fun func())
		GetVertexNoLock(txid *ledger.TransactionID) *vertex.WrappedTx
		GetVertex(txid *ledger.TransactionID) *vertex.WrappedTx
		AddVertexNoLock(vid *vertex.WrappedTx)
		StateStore() global.StateStore
		GetStateReaderForTheBranch(branch *vertex.WrappedTx) global.IndexedStateReader
		AddBranchNoLock(branch *vertex.WrappedTx)
		EvidenceIncomingBranch(txid *ledger.TransactionID, seqID ledger.ChainID)
		EvidenceBookedBranch(txid *ledger.TransactionID, seqID ledger.ChainID)
	}

	PullEnvironment interface {
		Pull(txid ledger.TransactionID)
		PokeMe(me, with *vertex.WrappedTx)
		PokeAllWith(wanted *vertex.WrappedTx)
	}
	PostEventEnvironment interface {
		PostEventNewGood(vid *vertex.WrappedTx)
		PostEventNewValidated(vid *vertex.WrappedTx)
	}

	Environment interface {
		global.Logging
		DAGAccessEnvironment
		PullEnvironment
		PostEventEnvironment
	}

	pastConeAttacher struct {
		Environment
		name                  string
		reason                error
		baselineBranch        *vertex.WrappedTx
		validPastVertices     set.Set[*vertex.WrappedTx]
		undefinedPastVertices set.Set[*vertex.WrappedTx]
		rooted                map[*vertex.WrappedTx]set.Set[byte]
		pokeMe                func(vid *vertex.WrappedTx)
		forceTrace1Ahead      bool
		prevCoverage          multistate.LedgerCoverage // set when baseline is determined
		coverageDelta         uint64
	}
)

func newPastConeAttacher(env Environment, name string) pastConeAttacher {
	return pastConeAttacher{
		Environment:           env,
		name:                  name,
		rooted:                make(map[*vertex.WrappedTx]set.Set[byte]),
		validPastVertices:     set.New[*vertex.WrappedTx](),
		undefinedPastVertices: set.New[*vertex.WrappedTx](),
		pokeMe:                func(_ *vertex.WrappedTx) {},
	}
}

func (a *pastConeAttacher) Name() string {
	return a.name
}

func (a *pastConeAttacher) baselineStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(a.GetStateReaderForTheBranch(a.baselineBranch))
}

func (a *pastConeAttacher) setReason(err error) {
	a.tracef("set reason: '%v'", err)
	a.reason = err
}

func (a *pastConeAttacher) pastConeVertexVisited(vid *vertex.WrappedTx, good bool) {
	if good {
		a.tracef("pastConeVertexVisited: %s is GOOD", vid.IDShortString)
		delete(a.undefinedPastVertices, vid)
		a.validPastVertices.Insert(vid)
	} else {
		util.Assertf(!a.validPastVertices.Contains(vid), "!a.validPastVertices.Contains(vid)")
		a.undefinedPastVertices.Insert(vid)
		a.tracef("pastConeVertexVisited: %s is UNDEF", vid.IDShortString)
	}
}

func (a *pastConeAttacher) isKnownVertex(vid *vertex.WrappedTx) bool {
	if a.validPastVertices.Contains(vid) {
		util.Assertf(!a.undefinedPastVertices.Contains(vid), "!a.undefinedPastVertices.Contains(vid)")
		return true
	}
	if a.undefinedPastVertices.Contains(vid) {
		util.Assertf(!a.validPastVertices.Contains(vid), "!a.validPastVertices.Contains(vid)")
		return true
	}
	return false
}

// solidifyBaseline directs attachment process down the DAG to reach the deterministically known baseline state
// for a sequencer milestone. Existence of it is guaranteed by the ledger constraints
func (a *pastConeAttacher) solidifyBaseline(v *vertex.Vertex) (ok bool) {
	if v.Tx.IsBranchTransaction() {
		return a.solidifyStemOfTheVertex(v)
	}
	return a.solidifySequencerBaseline(v)
}

func (a *pastConeAttacher) solidifyStemOfTheVertex(v *vertex.Vertex) (ok bool) {
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

func (a *pastConeAttacher) solidifySequencerBaseline(v *vertex.Vertex) (ok bool) {
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

// attachVertex: vid corresponds to the vertex v
// it solidifies vertex by traversing the past cone down to rooted tagAlongInputs or undefined vertices
// Repetitive calling of the function reaches all past vertices down to the rooted tagAlongInputs in the validPastVertices set, while
// the undefinedPastVertices set becomes empty This is the exit condition of the loop.
// It results in all validPastVertices are vertex.Good
// Otherwise, repetition reaches conflict (double spend) or vertex.Bad vertex and exits
func (a *pastConeAttacher) attachVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok bool) {
	util.Assertf(!v.Tx.IsSequencerMilestone() || v.FlagsUp(vertex.FlagBaselineSolid), "v.FlagsUp(vertex.FlagBaselineSolid) in %s", v.Tx.IDShortString)

	if visited.Contains(vid) {
		return true
	}
	visited.Insert(vid)

	a.tracef("attachVertex %s", vid.IDShortString)
	util.Assertf(!util.IsNil(a.baselineStateReader), "!util.IsNil(a.baselineStateReader)")
	if a.validPastVertices.Contains(vid) {
		return true
	}
	a.pastConeVertexVisited(vid, false) // undefined yet

	if !v.FlagsUp(vertex.FlagEndorsementsSolid) {
		a.tracef("endorsements not solid in %s\n", v.Tx.IDShortString())
		// depth-first along endorsements
		if !a.attachEndorsements(v, parasiticChainHorizon, visited) { // <<< recursive
			return false
		}
	}
	if v.FlagsUp(vertex.FlagEndorsementsSolid) {
		a.tracef("endorsements (%d) are all solid in %s", v.Tx.NumEndorsements(), v.Tx.IDShortString)
	} else {
		a.tracef("endorsements (%d) NOT solid in %s", v.Tx.NumEndorsements(), v.Tx.IDShortString)
	}
	inputsOk := a.attachInputsOfTheVertex(v, vid, parasiticChainHorizon, visited) // deep recursion
	if !inputsOk {
		return false
	}
	if v.FlagsUp(vertex.FlagAllInputsSolid) {
		// TODO nice-to-have optimization: constraints can be validated even before the vertex becomes good (solidified).
		//  It is enough to have all tagAlongInputs available, i.e. before full solidification

		alreadyValidated := v.FlagsUp(vertex.FlagConstraintsValid)
		if err := v.ValidateConstraints(); err != nil {
			a.setReason(err)
			a.tracef("%v", err)
			return false
		}
		if !alreadyValidated {
			a.PostEventNewValidated(vid)
		}
		a.tracef("constraints has been validated OK: %s", v.Tx.IDShortString())
		ok = true
	}
	if v.FlagsUp(vertex.FlagsSequencerVertexCompleted) {
		a.pastConeVertexVisited(vid, true)
	}
	return true
}

// Attaches endorsements of the vertex
func (a *pastConeAttacher) attachEndorsements(v *vertex.Vertex, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) bool {
	a.tracef("attachEndorsements %s", v.Tx.IDShortString)

	allGood := true
	for i, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			vidEndorsed = AttachTxID(v.Tx.EndorsementAt(byte(i)), a, OptionPullNonBranch, OptionInvokedBy(a.name))
			v.Endorsements[i] = vidEndorsed
		}
		baselineBranch := vidEndorsed.BaselineBranch()
		if baselineBranch != nil && !a.branchesCompatible(a.baselineBranch, baselineBranch) {
			a.setReason(fmt.Errorf("baseline %s of endorsement %s is incompatible with baseline state %s",
				baselineBranch.IDShortString(), vidEndorsed.IDShortString(), a.baselineBranch.IDShortString()))
			return false
		}

		endorsedStatus := vidEndorsed.GetTxStatus()
		if endorsedStatus == vertex.Bad {
			a.setReason(vidEndorsed.GetReason())
			return false
		}
		if a.validPastVertices.Contains(vidEndorsed) {
			// it means past cone of vidEndorsed is fully validated already
			continue
		}
		if endorsedStatus == vertex.Good {
			if !vidEndorsed.IsBranchTransaction() {
				// do not go behind branch
				// go deeper only if endorsement is good in order not to interfere with its sequencerAttacher
				ok := true
				vidEndorsed.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
					ok = a.attachVertex(v, vidEndorsed, parasiticChainHorizon, visited) // <<<<<<<<<<< recursion
				}})
				if !ok {
					return false
				}
			}
			a.tracef("endorsement is valid: %s", vidEndorsed.IDShortString)
		} else {
			a.tracef("endorsements are NOT all good in %s because of endorsed %s", v.Tx.IDShortString(), vidEndorsed.IDShortString())
			allGood = false

			// ask environment to poke this sequencerAttacher whenever something change with vidEndorsed
			a.pokeMe(vidEndorsed)
		}
	}
	if allGood {
		a.tracef("endorsements are all good in %s", v.Tx.IDShortString())
		v.SetFlagUp(vertex.FlagEndorsementsSolid)
	}
	return true
}

func (a *pastConeAttacher) attachInputsOfTheVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok bool) {
	a.tracef("attachInputsOfTheVertex in %s", vid.IDShortString)
	allInputsValidated := true
	notSolid := make([]byte, 0, v.Tx.NumInputs())
	var success bool
	for i := range v.Inputs {
		ok, success = a.attachInput(v, byte(i), vid, parasiticChainHorizon, visited)
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
		a.tracef("attachInputsOfTheVertex: not solid: in %s:\n%s", v.Tx.IDShortString(), linesSelectedInputs(v.Tx, notSolid).String())
	}
	return true
}

func linesSelectedInputs(tx *transaction.Transaction, indices []byte) *lines.Lines {
	ret := lines.New("      ")
	for _, i := range indices {
		ret.Add("#%d %s", i, util.Ref(tx.MustInputAt(i)).StringShort())
	}
	return ret
}

func (a *pastConeAttacher) attachInput(v *vertex.Vertex, inputIdx byte, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok, success bool) {
	a.tracef("attachInput #%d of %s", inputIdx, vid.IDShortString)
	if !a.attachInputID(v, vid, inputIdx) {
		a.tracef("bad input %d", inputIdx)
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

	if !a.attachOutput(wOut, parasiticChainHorizon, visited) {
		return false, false
	}
	success = a.validPastVertices.Contains(v.Inputs[inputIdx]) || a.isRooted(v.Inputs[inputIdx])
	if success {
		a.tracef("input #%d (%s) solidified", inputIdx, util.Ref(v.Tx.MustInputAt(inputIdx)).StringShort())
	}
	return true, success
}

func (a *pastConeAttacher) isRooted(vid *vertex.WrappedTx) bool {
	return len(a.rooted[vid]) > 0
}

func (a *pastConeAttacher) isValidated(vid *vertex.WrappedTx) bool {
	return a.validPastVertices.Contains(vid)
}

func (a *pastConeAttacher) attachRooted(wOut vertex.WrappedOutput) (ok bool, isRooted bool) {
	a.tracef("attachRooted %s", wOut.IDShortString)

	consumedRooted := a.rooted[wOut.VID]
	if consumedRooted.Contains(wOut.Index) {
		// it means it is already covered. The double spends are checked by attachInputID
		return true, true
	}
	stateReader := a.baselineStateReader()

	oid := wOut.DecodeID()
	txid := oid.TransactionID()
	if len(consumedRooted) == 0 && !stateReader.KnowsCommittedTransaction(&txid) {
		// it is not rooted, but it is fine
		return true, false
	}
	// it is rooted -> must be in the state
	// check if output is in the state
	if out := stateReader.GetOutput(oid); out != nil {
		// output has been found in the state -> Good
		ensured := wOut.VID.EnsureOutput(wOut.Index, out)
		util.Assertf(ensured, "ensureOutput: internal inconsistency")
		if len(consumedRooted) == 0 {
			consumedRooted = set.New[byte]()
		}
		consumedRooted.Insert(wOut.Index)
		a.rooted[wOut.VID] = consumedRooted
		// this is new rooted output -> add to the coverage delta
		a.coverageDelta += out.Amount()
		return true, true
	}
	// output has not been found in the state -> Bad
	err := fmt.Errorf("output %s is not in the state %s", wOut.IDShortString(), a.baselineBranch.IDShortString())
	a.setReason(err)
	a.tracef("%v", err)
	return false, false
}

func (a *pastConeAttacher) attachOutput(wOut vertex.WrappedOutput, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) bool {
	a.tracef("attachOutput %s", wOut.IDShortString)
	ok, isRooted := a.attachRooted(wOut)
	if !ok {
		return false
	}
	if isRooted {
		return true
	}

	if wOut.Timestamp().Before(parasiticChainHorizon) {
		// parasitic chain rule
		err := fmt.Errorf("parasitic chain threshold %s broken while attaching output %s", parasiticChainHorizon.String(), wOut.IDShortString())
		a.setReason(err)
		a.tracef("%v", err)
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
				ok = a.attachVertex(v, wOut.VID, parasiticChainHorizon, visited) // >>>>>>> recursion
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

func (a *pastConeAttacher) branchesCompatible(vid1, vid2 *vertex.WrappedTx) bool {
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

func (a *pastConeAttacher) attachInputID(consumerVertex *vertex.Vertex, consumerTx *vertex.WrappedTx, inputIdx byte) (ok bool) {
	inputOid := consumerVertex.Tx.MustInputAt(inputIdx)
	a.tracef("attachInputID: (oid = %s) #%d in %s", inputOid.StringShort, inputIdx, consumerTx.IDShortString)

	vidInputTx := consumerVertex.Inputs[inputIdx]
	if vidInputTx == nil {
		vidInputTx = AttachTxID(inputOid.TransactionID(), a, OptionInvokedBy(a.name))
	}
	util.Assertf(vidInputTx != nil, "vidInputTx != nil")

	//if vidInputTx.MutexWriteLocked_() {
	//	vidInputTx.GetTxStatus()
	//}
	if vidInputTx.GetTxStatus() == vertex.Bad {
		a.setReason(vidInputTx.GetReason())
		return false
	}
	// attach consumer and check for conflicts
	// CONFLICT DETECTION
	util.Assertf(a.isKnownVertex(consumerTx), "a.isKnownVertex(consumerTx)")

	a.tracef("before AttachConsumer of %s:\n       good: %s\n       undef: %s",
		inputOid.StringShort, vertex.VIDSetIDString(a.validPastVertices), vertex.VIDSetIDString(a.undefinedPastVertices))

	if !vidInputTx.AttachConsumer(inputOid.Index(), consumerTx, a.checkConflictsFunc(consumerTx)) {
		err := fmt.Errorf("input %s of consumer %s conflicts with existing consumers in the baseline state %s (double spend)",
			inputOid.StringShort(), consumerTx.IDShortString(), a.baselineBranch.IDShortString())
		a.setReason(err)
		a.tracef("%v", err)
		return false
	}
	a.tracef("attached consumer %s of %s", consumerTx.IDShortString, inputOid.StringShort)

	if vidInputTx.IsSequencerMilestone() {
		// for sequencer milestones check if baselines are compatible
		if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
			if !a.branchesCompatible(a.baselineBranch, inputBaselineBranch) {
				err := fmt.Errorf("branches %s and %s not compatible", a.baselineBranch.IDShortString(), inputBaselineBranch.IDShortString())
				a.setReason(err)
				a.tracef("%v", err)
				return false
			}
		}
	}
	consumerVertex.Inputs[inputIdx] = vidInputTx
	return true
}

func (a *pastConeAttacher) checkConflictsFunc(consumerTx *vertex.WrappedTx) func(existingConsumers set.Set[*vertex.WrappedTx]) bool {
	return func(existingConsumers set.Set[*vertex.WrappedTx]) (conflict bool) {
		existingConsumers.ForEach(func(existingConsumer *vertex.WrappedTx) bool {
			if existingConsumer == consumerTx {
				return true
			}
			if a.validPastVertices.Contains(existingConsumer) {
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

func (a *pastConeAttacher) setBaselineBranch(vid *vertex.WrappedTx) {
	a.baselineBranch = vid
	if a.baselineBranch != nil {
		if multistate.HistoryCoverageDeltas > 1 {
			rr, found := multistate.FetchRootRecord(a.StateStore(), a.baselineBranch.ID)
			util.Assertf(found, "setBaselineBranch: can't fetch root record for %s", a.baselineBranch.IDShortString())

			a.prevCoverage = rr.LedgerCoverage
		}
	}
}

func (a *pastConeAttacher) ledgerCoverage(currentTS ledger.LogicalTime) multistate.LedgerCoverage {
	return a.prevCoverage.MakeNext(int(currentTS.Slot())-int(a.baselineBranch.Slot())+1, a.coverageDelta)
}

// not thread safe
var trace = false

func SetTraceOn() {
	trace = true
}

func (a *pastConeAttacher) trace1Ahead() {
	a.forceTrace1Ahead = true
}

func (a *pastConeAttacher) tracef(format string, lazyArgs ...any) {
	if trace || a.forceTrace1Ahead {
		format1 := "TRACE [sequencerAttacher " + a.name + "] " + format
		a.Log().Infof(format1, util.EvalLazyArgs(lazyArgs...)...)
		a.forceTrace1Ahead = false
	}
}

func tracef(env Environment, format string, lazyArgs ...any) {
	if trace {
		env.Log().Infof("TRACE "+format, util.EvalLazyArgs(lazyArgs...)...)
	}
}
