package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
)

func (a *attacher) finalize() {
	a.tracef("finalize")
	util.Assertf(len(a.undefinedPastVertices) == 1 && a.undefinedPastVertices.Contains(a.vid), "undefinedPastVertices set is inconsistent",
		func() any { return vertex.VerticesLines(util.Keys(a.undefinedPastVertices)).String() })
	util.Assertf(len(a.pendingOutputs) == 0, "len(a.pendingOutputs)==0")
	util.Assertf(len(a.rooted) > 0, "len(a.rooted) > 0")
	util.Assertf(len(a.validPastVertices) > 0, "len(a.validPastVertices) > 0")
	util.AssertNoError(a.checkPastConeVerticesConsistent())

	if a.vid.IsBranchTransaction() {
		coverage := a.commitBranch()
		a.vid.SetLedgerCoverage(coverage)
		a.env.WithGlobalWriteLock(func() {
			a.env.AddBranchNoLock(a.vid)
		})
		a.env.EvidenceBookedBranch(a.vid.ID(), a.vid.MustSequencerID())
		a.tracef("finalized branch")
	} else {
		coverage := a.calculateCoverage()
		a.vid.SetLedgerCoverage(coverage)
		a.tracef("finalized sequencer milestone")
	}
}

func (a *attacher) commitBranch() multistate.LedgerCoverage {
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
		}
	}
	// generate ADD TX and ADD OUTPUT mutations
	for vid := range a.validPastVertices {
		muts.InsertAddTxMutation(*vid.ID(), a.vid.TimeSlot())

		// ADD OUTPUT mutations only for not consumed outputs
		producedOutputIndices := vid.NotConsumedOutputIndices(a.validPastVertices)
		for _, idx := range producedOutputIndices {
			muts.InsertAddOutputMutation(vid.OutputID(idx), vid.MustOutputAt(idx))
		}
	}

	//fmt.Printf("mutations:\n%s\n", muts.Lines("    ").String())

	seqID, stemOID := a.vid.MustSequencerIDAndStemID()
	upd := multistate.MustNewUpdatable(a.env.StateStore(), a.baselineStateReader().Root())
	coverage := a.ledgerCoverage(coverageDelta)
	upd.MustUpdate(muts, &stemOID, &seqID, coverage)
	return coverage
}

func (a *attacher) ledgerCoverage(coverageDelta uint64) multistate.LedgerCoverage {
	var prevCoverage multistate.LedgerCoverage
	if multistate.HistoryCoverageDeltas > 1 {
		rr, found := multistate.FetchRootRecord(a.env.StateStore(), *a.baselineBranch.ID())
		util.Assertf(found, "can't fetch root record for %s", a.baselineBranch.IDShortString())

		prevCoverage = rr.LedgerCoverage
	}
	return prevCoverage.MakeNext(int(a.vid.TimeSlot())-int(a.baselineBranch.TimeSlot())+1, coverageDelta)
}

func (a *attacher) calculateCoverage() multistate.LedgerCoverage {
	coverageDelta := uint64(0)

	for vid, consumed := range a.rooted {
		util.Assertf(len(consumed) > 0, "len(consumed)>0")
		for idx := range consumed {
			coverageDelta += vid.MustOutputAt(idx).Amount()
		}
	}
	return a.ledgerCoverage(coverageDelta)
}

func (a *attacher) checkPastConeVerticesConsistent() (err error) {
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
			if !v.ConstraintsValid {
				err = fmt.Errorf("%s is not validated yet", v.Tx.IDShortString())
			}
		}})
	}
	return
}
