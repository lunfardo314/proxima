package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

// enforceConsistencyWithTxMetadata :
// if true, node is crashed immediately upon inconsistency with provided transaction metadata
// if false, an error is reported
// The transaction metadata is optionally provided together with the sequencer transaction bytes by other nodes
// or by the tx store, therefore is not trust-less.
// A malicious node could crash other peers by sending inconsistent metadata,
// therefore in the production environment enforceConsistencyWithTxMetadata should be false
// and the connection with the malicious peer should be immediately severed
const enforceConsistencyWithTxMetadata = true

func (a *milestoneAttacher) finalize() {
	a.Tracef(TraceTagAttachMilestone, "finalize")

	// check resulting ledger baselineCoverage is equal to the baselineCoverage in metadata, if provided
	if a.metadata != nil && a.metadata.LedgerCoverageDelta != nil {
		if *a.metadata.LedgerCoverageDelta != a.coverage.LatestDelta() {
			err := fmt.Errorf("commitBranch %s: major inconsistency: ledger baselineCoverage delta not equal to the baselineCoverage delta provided in metadata", a.vid.IDShortString())
			if enforceConsistencyWithTxMetadata {
				a.Log().Fatal(err)
			} else {
				a.Log().Error(err)
			}
		}
	}

	a.finals.baseline = a.baseline
	a.finals.numTransactions = len(a.vertices)

	//tmp := a.coverage.MakeNext(int(a.vid.Slot() - a.baseline.Slot()))
	//if tmp.Sum() == 10_000_000 {
	//	fmt.Printf(">>>>>>>>>>>>>>>>>>>>>> 10_000_000 %s\n", a.vid.IDShortString())
	//}
	//fmt.Printf("%s\n", a.dumpLines("   ").String())

	a.finals.coverage = a.coverage.MakeNext(int(a.vid.Slot() - a.baseline.Slot()))
	util.Assertf(!a.vid.IsBranchTransaction() || a.finals.coverage.LatestDelta() == 0, "final baselineCoverage of the branch must have latest delta == 0")

	a.Tracef(TraceTagAttachMilestone, "set ledger baselineCoverage in %s to %s", a.vid.IDShortString(), a.finals.coverage.String())
	a.vid.SetLedgerCoverage(a.finals.coverage)

	if a.vid.IsBranchTransaction() {
		a.commitBranch()

		a.WithGlobalWriteLock(func() {
			a.AddBranchNoLock(a.vid)
		})
		a.EvidenceBookedBranch(&a.vid.ID, a.vid.MustSequencerID())
		a.Tracef(TraceTagAttachMilestone, "finalized branch")
	} else {
		a.Tracef(TraceTagAttachMilestone, "finalized sequencer milestone")
	}
}

func (a *milestoneAttacher) commitBranch() {
	util.Assertf(a.vid.IsBranchTransaction(), "a.vid.IsBranchTransaction()")

	muts := multistate.NewMutations()

	// generate DEL mutations
	for vid, consumed := range a.rooted {
		util.Assertf(len(consumed) > 0, "len(consumed)>0")
		for idx := range consumed {
			out := vid.MustOutputWithIDAt(idx)
			muts.InsertDelOutputMutation(out.ID)
			a.finals.numDeletedOutputs++
		}
	}
	// generate ADD TX and ADD OUTPUT mutations
	allVerticesSet := set.NewFromKeys(a.vertices)
	for vid := range a.vertices {
		if a.isKnownRooted(vid) {
			continue
		}
		muts.InsertAddTxMutation(vid.ID, a.vid.Slot(), byte(vid.NumProducedOutputs()-1))
		// ADD OUTPUT mutations only for not consumed outputs
		producedOutputIndices := vid.NotConsumedOutputIndices(allVerticesSet)
		for _, idx := range producedOutputIndices {
			muts.InsertAddOutputMutation(vid.OutputID(idx), vid.MustOutputAt(idx))
			a.finals.numCreatedOutputs++
		}
	}

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()
	upd := multistate.MustNewUpdatable(a.StateStore(), a.baselineStateReader().Root())
	upd.MustUpdate(muts, &stemOID, &seqID, *a.vid.GetLedgerCoverage())
	a.finals.root = upd.Root()

	// check consistency with metadata
	if a.metadata != nil && !util.IsNil(a.metadata.StateRoot) {
		if !ledger.CommitmentModel.EqualCommitments(a.finals.root, a.metadata.StateRoot) {
			err := fmt.Errorf("commitBranch %s: major inconsistency: state root not equal to the state root provided in metadata", a.vid.IDShortString())
			if enforceConsistencyWithTxMetadata {
				a.Log().Fatal(err)
			} else {
				a.Log().Error(err)
			}
		}
	}
}

func (a *milestoneAttacher) checkConsistencyBeforeFinalization() error {
	err := a._checkConsistencyBeforeFinalization()
	if err != nil {
		err = fmt.Errorf("checkConsistencyBeforeFinalization in attacher %s: %w\n%s", a.name, err, a.dumpLines("       "))
	}
	return err
}

func (a *milestoneAttacher) _checkConsistencyBeforeFinalization() (err error) {
	if a.containsUndefined() {
		return fmt.Errorf("still contains undefined vertices")
	}
	// should be at least one rooted output ( ledger baselineCoverage must be > 0)
	if len(a.rooted) == 0 {
		return fmt.Errorf("at least one rooted output is expected")
	}
	for vid := range a.rooted {
		if !a.isKnownDefined(vid) {
			return fmt.Errorf("all rooted must be defined. This is not: %s", vid.IDShortString())
		}
	}
	if len(a.vertices) == 0 {
		return fmt.Errorf("vertices is empty")
	}
	sumRooted := uint64(0)
	for vid, consumed := range a.rooted {
		var o *ledger.Output
		consumed.ForEach(func(idx byte) bool {
			o, err = vid.OutputAt(idx)
			if err != nil {
				return false
			}
			sumRooted += o.Amount()
			return true
		})
	}
	if err != nil {
		return
	}
	if sumRooted == 0 {
		err = fmt.Errorf("sum of rooted cannot be 0")
		return
	}
	if sumRooted != a.coverage.LatestDelta() {
		err = fmt.Errorf("sum of amounts of rooted outputs %s is not equal to the coverage sumRooted %s",
			util.GoTh(sumRooted), util.GoTh(a.coverage.LatestDelta()))
	}

	for vid, flags := range a.vertices {
		if !flags.FlagsUp(FlagKnown | FlagDefined) {
			return fmt.Errorf("wrong flags %08b in %s", flags, vid.IDShortString())
		}
		if vid == a.vid {
			if vid.GetTxStatus() == vertex.Bad {
				return fmt.Errorf("vertex %s is BAD", vid.IDShortString())
			}
			continue
		}
		status := vid.GetTxStatus()
		if status == vertex.Bad {
			return fmt.Errorf("BAD vertex in the past cone: %s", vid.IDShortString())
		}
		if status != vertex.Good && vid.IsSequencerMilestone() {
			return fmt.Errorf("UNDEFINED sequencer vertex in the past cone: %s", vid.IDShortString())
		}

		vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			missingInputs, missingEndorsements := v.NumMissingInputs()
			if missingInputs+missingEndorsements > 0 {
				err = fmt.Errorf("not all dependencies solid. Missing tagAlongInputs: %d, missing endorsements: %d, missing input tx:\n%s",
					missingInputs, missingEndorsements, v.MissingInputTxIDString())
			}
		}})
	}

	a.vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
		v.ForEachEndorsement(func(i byte, vidEndorsed *vertex.WrappedTx) bool {
			lc := vidEndorsed.GetLedgerCoverage()
			if lc == nil {
				err = fmt.Errorf("coverage not set in the endorsed %s", vidEndorsed.IDShortString())
				return false
			}
			if a.coverage.LatestDelta() < lc.LatestDelta() {
				err = fmt.Errorf("coverage delta should not decrease.\nGot: delta(%s) at %s <= delta(%s) in %s",
					util.GoTh(a.coverage.LatestDelta()), a.vid.Timestamp().String(), util.GoTh(lc.LatestDelta()), vidEndorsed.IDShortString())
				return false
			}
			return true
		})
	}})
	return
}
