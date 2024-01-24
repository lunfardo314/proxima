package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
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

	// check resulting ledger coverage is equal to the coverage in metadata, if provided
	if a.metadata != nil && a.metadata.LedgerCoverageDelta != nil {
		if *a.metadata.LedgerCoverageDelta != a.coverageDelta {
			err := fmt.Errorf("commitBranch %s: major inconsistency: ledger coverage delta not equal to the coverage delta provided in metadata", a.vid.IDShortString())
			if enforceConsistencyWithTxMetadata {
				a.Log().Fatal(err)
			} else {
				a.Log().Error(err)
			}
		}
	}

	a.finals.baseline = a.baselineBranch
	a.finals.numTransactions = len(a.validPastVertices)
	a.finals.coverage = a.ledgerCoverage(a.vid.Timestamp())
	a.Tracef(TraceTagAttachMilestone, "set ledger coverage in %s to %s", a.vid.IDShortString(), a.finals.coverage.String())
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
	for vid := range a.validPastVertices {
		muts.InsertAddTxMutation(vid.ID, a.vid.Slot(), byte(vid.NumProducedOutputs()-1))
		// ADD OUTPUT mutations only for not consumed outputs
		producedOutputIndices := vid.NotConsumedOutputIndices(a.validPastVertices)
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

func (a *milestoneAttacher) checkPastConeVerticesConsistent() (err error) {
	defer a.Log().Sync()

	if len(a.undefinedPastVertices) != 0 {
		return fmt.Errorf("undefinedPastVertices should be empty. Got: {%s}", vertex.VIDSetIDString(a.undefinedPastVertices))
	}
	// should be at least one rooted output ( ledger coverage must be > 0)
	if len(a.rooted) == 0 {
		return fmt.Errorf("at least one rooted output is expected")
	}

	if len(a.validPastVertices) == 0 {
		return fmt.Errorf("validPastVertices is empty")
	}
	for vid := range a.validPastVertices {
		if vid == a.vid {
			if vid.GetTxStatus() == vertex.Bad {
				return fmt.Errorf("vertex %s is bad", vid.IDShortString())
			}
			continue
		}
		status := vid.GetTxStatus()
		if status == vertex.Bad || (status != vertex.Good && vid.IsSequencerMilestone()) {
			return fmt.Errorf("inconsistent vertex (%s) in the past cone: %s",
				status.String(), vid.IDShortString())
		}
		vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			if !v.FlagsUp(vertex.FlagsSequencerVertexCompleted) {
				err = fmt.Errorf("%s is not completed yet. Flags: %08b", v.Tx.IDShortString(), v.Flags)
			}
			missingInputs, missingEndorsements := v.NumMissingInputs()
			if missingInputs+missingEndorsements > 0 {
				err = fmt.Errorf("not all dependencies solid. Missing tagAlongInputs: %d, missing endorsements: %d, missing input tx:\n%s",
					missingInputs, missingEndorsements, v.MissingInputTxIDString())
			}
		}})
	}
	return
}
