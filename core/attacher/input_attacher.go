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

func (ia *inputAttacher) baselineStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(ia.env.GetStateReaderForTheBranch(ia.baselineBranch))
}

func (ia *inputAttacher) setReason(err error) {
	ia.tracef("set reason: '%v'", err)
	ia.reason = err
}

func (ia *inputAttacher) pastConeVertexVisited(vid *vertex.WrappedTx, good bool) {
	if good {
		ia.tracef("pastConeVertexVisited: %s is GOOD", vid.IDShortString)
		delete(ia.undefinedPastVertices, vid)
		ia.validPastVertices.Insert(vid)
	} else {
		util.Assertf(!ia.validPastVertices.Contains(vid), "!ia.validPastVertices.Contains(vid)")
		ia.undefinedPastVertices.Insert(vid)
		ia.tracef("pastConeVertexVisited: %s is UNDEF", vid.IDShortString)
	}
}

func (ia *inputAttacher) isKnownVertex(vid *vertex.WrappedTx) bool {
	if ia.validPastVertices.Contains(vid) {
		util.Assertf(!ia.undefinedPastVertices.Contains(vid), "!ia.undefinedPastVertices.Contains(vid)")
		return true
	}
	if ia.undefinedPastVertices.Contains(vid) {
		util.Assertf(!ia.validPastVertices.Contains(vid), "!ia.validPastVertices.Contains(vid)")
		return true
	}
	return false
}

// attachVertex: vid corresponds to the vertex v
// it solidifies vertex by traversing the past cone down to rooted inputs or undefined vertices
// Repetitive calling of the function reaches all past vertices down to the rooted inputs in the validPastVertices set, while
// the undefinedPastVertices set becomes empty This is the exit condition of the loop.
// It results in all validPastVertices are vertex.Good
// Otherwise, repetition reaches conflict (double spend) or vertex.Bad vertex and exits
func (ia *inputAttacher) attachVertex(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok bool) {
	util.Assertf(!v.Tx.IsSequencerMilestone() || v.FlagsUp(vertex.FlagBaselineSolid), "v.FlagsUp(vertex.FlagBaselineSolid) in %s", v.Tx.IDShortString)

	if visited.Contains(vid) {
		return true
	}
	visited.Insert(vid)

	ia.tracef("attachVertex %s", vid.IDShortString)
	util.Assertf(!util.IsNil(ia.baselineStateReader), "!util.IsNil(ia.baselineStateReader)")
	if ia.validPastVertices.Contains(vid) {
		return true
	}
	ia.pastConeVertexVisited(vid, false) // undefined yet

	if !v.FlagsUp(vertex.FlagEndorsementsSolid) {
		ia.tracef("endorsements not solid in %s\n", v.Tx.IDShortString())
		// depth-first along endorsements
		if !ia.attachEndorsements(v, parasiticChainHorizon, visited) { // <<< recursive
			return false
		}
	}
	if v.FlagsUp(vertex.FlagEndorsementsSolid) {
		ia.tracef("endorsements (%d) are all solid in %s", v.Tx.NumEndorsements(), v.Tx.IDShortString)
	} else {
		ia.tracef("endorsements (%d) NOT solid in %s", v.Tx.NumEndorsements(), v.Tx.IDShortString)
	}
	inputsOk := ia.attachInputs(v, vid, parasiticChainHorizon, visited) // deep recursion
	if !inputsOk {
		return false
	}
	if v.FlagsUp(vertex.FlagAllInputsSolid) {
		// TODO nice-to-have optimization: constraints can be validated even before the vertex becomes good (solidified).
		//  It is enough to have all inputs available, i.e. before full solidification

		alreadyValidated := v.FlagsUp(vertex.FlagConstraintsValid)
		if err := v.ValidateConstraints(); err != nil {
			ia.setReason(err)
			ia.tracef("%v", err)
			return false
		}
		if !alreadyValidated {
			ia.env.PostEventNewValidated(vid)
		}
		ia.tracef("constraints has been validated OK: %s", v.Tx.IDShortString())
		ok = true
	}
	if v.FlagsUp(vertex.FlagsSequencerVertexCompleted) {
		ia.pastConeVertexVisited(vid, true)
	}
	return true
}

// Attaches endorsements of the vertex
func (ia *inputAttacher) attachEndorsements(v *vertex.Vertex, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) bool {
	ia.tracef("attachEndorsements %s", v.Tx.IDShortString)

	allGood := true
	for i, vidEndorsed := range v.Endorsements {
		if vidEndorsed == nil {
			vidEndorsed = AttachTxID(v.Tx.EndorsementAt(byte(i)), ia.env, OptionPullNonBranch, OptionInvokedBy(ia.name))
			v.Endorsements[i] = vidEndorsed
		}
		baselineBranch := vidEndorsed.BaselineBranch()
		if baselineBranch != nil && !ia.branchesCompatible(ia.baselineBranch, baselineBranch) {
			ia.setReason(fmt.Errorf("baseline %s of endorsement %s is incompatible with baseline state %s",
				baselineBranch.IDShortString(), vidEndorsed.IDShortString(), ia.baselineBranch.IDShortString()))
			return false
		}

		endorsedStatus := vidEndorsed.GetTxStatus()
		if endorsedStatus == vertex.Bad {
			ia.setReason(vidEndorsed.GetReason())
			return false
		}
		if ia.validPastVertices.Contains(vidEndorsed) {
			// it means past cone of vidEndorsed is fully validated already
			continue
		}
		if endorsedStatus == vertex.Good {
			if !vidEndorsed.IsBranchTransaction() {
				// do not go behind branch
				// go deeper only if endorsement is good in order not to interfere with its attacher
				ok := true
				vidEndorsed.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
					ok = ia.attachVertex(v, vidEndorsed, parasiticChainHorizon, visited) // <<<<<<<<<<< recursion
				}})
				if !ok {
					return false
				}
			}
			ia.tracef("endorsement is valid: %s", vidEndorsed.IDShortString)
		} else {
			ia.tracef("endorsements are NOT all good in %s because of endorsed %s", v.Tx.IDShortString(), vidEndorsed.IDShortString())
			allGood = false

			// ask environment to poke this attacher whenever something change with vidEndorsed
			ia.pokeMe(vidEndorsed)
		}
	}
	if allGood {
		ia.tracef("endorsements are all good in %s", v.Tx.IDShortString())
		v.SetFlagUp(vertex.FlagEndorsementsSolid)
	}
	return true
}

func (ia *inputAttacher) attachInputs(v *vertex.Vertex, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok bool) {
	ia.tracef("attachInputs in %s", vid.IDShortString)
	allInputsValidated := true
	notSolid := make([]byte, 0, v.Tx.NumInputs())
	var success bool
	for i := range v.Inputs {
		ok, success = ia.attachInput(v, byte(i), vid, parasiticChainHorizon, visited)
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
			ia.env.PokeAllWith(vid)
		}
	} else {
		ia.tracef("attachInputs: not solid: in %s:\n%s", v.Tx.IDShortString(), linesSelectedInputs(v.Tx, notSolid).String())
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

func (ia *inputAttacher) attachInput(v *vertex.Vertex, inputIdx byte, vid *vertex.WrappedTx, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) (ok, success bool) {
	ia.tracef("attachInput #%d of %s", inputIdx, vid.IDShortString)
	if !ia.attachInputID(v, vid, inputIdx) {
		ia.tracef("bad input %d", inputIdx)
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

	if !ia.attachOutput(wOut, v.Tx.ID(), parasiticChainHorizon, visited) {
		return false, false
	}
	success = ia.validPastVertices.Contains(v.Inputs[inputIdx]) || ia.isRooted(v.Inputs[inputIdx])
	if success {
		ia.tracef("input #%d (%s) solidified", inputIdx, util.Ref(v.Tx.MustInputAt(inputIdx)).StringShort())
	}
	return true, success
}

func (ia *inputAttacher) isRooted(vid *vertex.WrappedTx) bool {
	return len(ia.rooted[vid]) > 0
}

func (ia *inputAttacher) isValidated(vid *vertex.WrappedTx) bool {
	return ia.validPastVertices.Contains(vid)
}

func (ia *inputAttacher) attachRooted(wOut vertex.WrappedOutput, consumerTxID *ledger.TransactionID) (ok bool, isRooted bool) {
	ia.tracef("attachRooted %s", wOut.IDShortString)

	consumedRooted := ia.rooted[wOut.VID]
	if consumedRooted.Contains(wOut.Index) {
		// it means it is already covered. The double spends are checked by attachInputID
		return true, true
	}
	// not ia double spend
	stateReader := ia.baselineStateReader()

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
		ia.rooted[wOut.VID] = consumedRooted
		return true, true
	}
	// output has not been found in the state -> Bad
	err := fmt.Errorf("ouput %s (consumed by %s) is not in the state %s", wOut.IDShortString(), consumerTxID.StringShort(), ia.baselineBranch.IDShortString())
	ia.setReason(err)
	ia.tracef("%v", err)
	return false, false
}

func (ia *inputAttacher) attachOutput(wOut vertex.WrappedOutput, consumerTxID *ledger.TransactionID, parasiticChainHorizon ledger.LogicalTime, visited set.Set[*vertex.WrappedTx]) bool {
	ia.tracef("attachOutput %s", wOut.IDShortString)
	ok, isRooted := ia.attachRooted(wOut, consumerTxID)
	if !ok {
		return false
	}
	if isRooted {
		return true
	}

	if wOut.Timestamp().Before(parasiticChainHorizon) {
		// parasitic chain rule
		err := fmt.Errorf("parasitic chain threshold %s broken while attaching output %s", parasiticChainHorizon.String(), wOut.IDShortString())
		ia.setReason(err)
		ia.tracef("%v", err)
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
				ok = ia.attachVertex(v, wOut.VID, parasiticChainHorizon, visited) // >>>>>>> recursion
			} else {
				// not ia seq milestone OR is not good yet -> don't go deeper
				ia.pokeMe(wOut.VID)
			}
		},
		VirtualTx: func(v *vertex.VirtualTransaction) {
			ia.env.Pull(wOut.VID.ID)
			// ask environment to poke when transaction arrive
			ia.pokeMe(wOut.VID)
		},
	})
	if !ok {
		return false
	}
	return true
}

func (ia *inputAttacher) branchesCompatible(vid1, vid2 *vertex.WrappedTx) bool {
	util.Assertf(vid1 != nil && vid2 != nil, "vid1 != nil && vid2 != nil")
	util.Assertf(vid1.IsBranchTransaction() && vid2.IsBranchTransaction(), "vid1.IsBranchTransaction() && vid2.IsBranchTransaction()")
	switch {
	case vid1 == vid2:
		return true
	case vid1.Slot() == vid2.Slot():
		// two different branches on the same slot conflicts
		return false
	case vid1.Slot() < vid2.Slot():
		return multistate.BranchIsDescendantOf(&vid2.ID, &vid1.ID, ia.env.StateStore)
	default:
		return multistate.BranchIsDescendantOf(&vid1.ID, &vid2.ID, ia.env.StateStore)
	}
}

func (ia *inputAttacher) attachInputID(consumerVertex *vertex.Vertex, consumerTx *vertex.WrappedTx, inputIdx byte) (ok bool) {
	inputOid := consumerVertex.Tx.MustInputAt(inputIdx)
	ia.tracef("attachInputID: (oid = %s) #%d in %s", inputOid.StringShort, inputIdx, consumerTx.IDShortString)

	vidInputTx := consumerVertex.Inputs[inputIdx]
	if vidInputTx == nil {
		vidInputTx = AttachTxID(inputOid.TransactionID(), ia.env, OptionInvokedBy(ia.name))
	}
	util.Assertf(vidInputTx != nil, "vidInputTx != nil")

	//if vidInputTx.MutexWriteLocked_() {
	//	vidInputTx.GetTxStatus()
	//}
	if vidInputTx.GetTxStatus() == vertex.Bad {
		ia.setReason(vidInputTx.GetReason())
		return false
	}
	// attach consumer and check for conflicts
	// CONFLICT DETECTION
	util.Assertf(ia.isKnownVertex(consumerTx), "ia.isKnownVertex(consumerTx)")

	ia.tracef("before AttachConsumer of %s:\n       good: %s\n       undef: %s",
		inputOid.StringShort, vertex.VIDSetIDString(ia.validPastVertices), vertex.VIDSetIDString(ia.undefinedPastVertices))

	if !vidInputTx.AttachConsumer(inputOid.Index(), consumerTx, ia.checkConflictsFunc(consumerTx)) {
		err := fmt.Errorf("input %s of consumer %s conflicts with existing consumers in the baseline state %s (double spend)",
			inputOid.StringShort(), consumerTx.IDShortString(), ia.baselineBranch.IDShortString())
		ia.setReason(err)
		ia.tracef("%v", err)
		return false
	}
	ia.tracef("attached consumer %s of %s", consumerTx.IDShortString, inputOid.StringShort)

	if vidInputTx.IsSequencerMilestone() {
		// for sequencer milestones check if baselines are compatible
		if inputBaselineBranch := vidInputTx.BaselineBranch(); inputBaselineBranch != nil {
			if !ia.branchesCompatible(ia.baselineBranch, inputBaselineBranch) {
				err := fmt.Errorf("branches %s and %s not compatible", ia.baselineBranch.IDShortString(), inputBaselineBranch.IDShortString())
				ia.setReason(err)
				ia.tracef("%v", err)
				return false
			}
		}
	}
	consumerVertex.Inputs[inputIdx] = vidInputTx
	return true
}

func (ia *inputAttacher) checkConflictsFunc(consumerTx *vertex.WrappedTx) func(existingConsumers set.Set[*vertex.WrappedTx]) bool {
	return func(existingConsumers set.Set[*vertex.WrappedTx]) (conflict bool) {
		existingConsumers.ForEach(func(existingConsumer *vertex.WrappedTx) bool {
			if existingConsumer == consumerTx {
				return true
			}
			if ia.validPastVertices.Contains(existingConsumer) {
				conflict = true
				return false
			}
			if ia.undefinedPastVertices.Contains(existingConsumer) {
				conflict = true
				return false
			}
			return true
		})
		return
	}
}

// not thread safe
var trace = false

func SetTraceOn() {
	trace = true
}

func (ia *inputAttacher) trace1Ahead() {
	ia.forceTrace1Ahead = true
}

func (ia *inputAttacher) tracef(format string, lazyArgs ...any) {
	if trace || ia.forceTrace1Ahead {
		format1 := "TRACE [attacher " + ia.name + "] " + format
		ia.env.Log().Infof(format1, util.EvalLazyArgs(lazyArgs...)...)
		ia.forceTrace1Ahead = false
	}
}

func tracef(env Environment, format string, lazyArgs ...any) {
	if trace {
		env.Log().Infof("TRACE "+format, util.EvalLazyArgs(lazyArgs...)...)
	}
}
