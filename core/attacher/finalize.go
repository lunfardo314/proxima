package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

func (a *sequencerAttacher) finalize() {
	a.tracef("finalize")

	if a.vid.IsBranchTransaction() {
		a.commitBranch()
		a.vid.SetLedgerCoverage(a.stats.coverage)
		a.env.WithGlobalWriteLock(func() {
			a.env.AddBranchNoLock(a.vid)
		})
		a.env.EvidenceBookedBranch(&a.vid.ID, a.vid.MustSequencerID())
		a.tracef("finalized branch")
	} else {
		a.calculateSequencerTxStats()
		a.vid.SetLedgerCoverage(a.stats.coverage)
		a.tracef("finalized sequencer milestone")
	}
}

func (a *sequencerAttacher) commitBranch() {
	util.Assertf(a.vid.IsBranchTransaction(), "a.vid.IsBranchTransaction()")

	muts := multistate.NewMutations()
	coverageDelta := uint64(0)

	// generate DEL mutations
	for vid, consumed := range a.rooted {
		util.Assertf(len(consumed) > 0, "len(consumed)>0")
		for idx := range consumed {
			out := vid.MustOutputWithIDAt(idx)
			coverageDelta += out.Output.Amount()
			muts.InsertDelOutputMutation(out.ID)
			a.stats.numDeletedOutputs++
		}
	}
	// generate ADD TX and ADD OUTPUT mutations
	for vid := range a.validPastVertices {
		muts.InsertAddTxMutation(vid.ID, a.vid.Slot(), byte(vid.NumProducedOutputs()-1))
		a.stats.numTransactions++

		// ADD OUTPUT mutations only for not consumed outputs
		producedOutputIndices := vid.NotConsumedOutputIndices(a.validPastVertices)
		for _, idx := range producedOutputIndices {
			muts.InsertAddOutputMutation(vid.OutputID(idx), vid.MustOutputAt(idx))
			a.stats.numCreatedOutputs++
		}
	}

	//a.trace1Ahead()
	//a.tracef("mutations:\n%s", muts.Lines("    ").String())

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()
	upd := multistate.MustNewUpdatable(a.env.StateStore(), a.baselineStateReader().Root())
	a.stats.coverage = a.ledgerCoverage(coverageDelta)
	upd.MustUpdate(muts, &stemOID, &seqID, a.stats.coverage)
}

func (a *sequencerAttacher) ledgerCoverage(coverageDelta uint64) multistate.LedgerCoverage {
	var prevCoverage multistate.LedgerCoverage
	if multistate.HistoryCoverageDeltas > 1 {
		rr, found := multistate.FetchRootRecord(a.env.StateStore(), a.baselineBranch.ID)
		util.Assertf(found, "can't fetch root record for %s", a.baselineBranch.IDShortString())

		prevCoverage = rr.LedgerCoverage
	}
	return prevCoverage.MakeNext(int(a.vid.Slot())-int(a.baselineBranch.Slot())+1, coverageDelta)
}

func (a *sequencerAttacher) calculateSequencerTxStats() {
	coverageDelta := uint64(0)

	for vid, consumed := range a.rooted {
		util.Assertf(len(consumed) > 0, "len(consumed)>0")
		for idx := range consumed {
			coverageDelta += vid.MustOutputAt(idx).Amount()
		}
	}
	a.stats.numTransactions = len(a.validPastVertices)
	a.stats.numCreatedOutputs = len(a.rooted)
	a.stats.coverage = a.ledgerCoverage(coverageDelta)
}

func (a *sequencerAttacher) checkPastConeVerticesConsistent() (err error) {
	defer a.env.Log().Sync()

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
		vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			if !v.FlagsUp(vertex.FlagsSequencerVertexCompleted) {
				err = fmt.Errorf("%s is not completed yet. Flags: %08b", v.Tx.IDShortString(), v.Flags)
			}
			missingInputs, missingEndorsements := v.NumMissingInputs()
			if missingInputs+missingEndorsements > 0 {
				err = fmt.Errorf("not all dependencies solid. Missing inputs: %d, missing endorsements: %d, missing input tx:\n%s",
					missingInputs, missingEndorsements, v.MissingInputTxIDString())
			}
		}})
	}
	return
}
