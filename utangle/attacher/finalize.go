package attacher

import (
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
)

func (a *attacher) finalize() {
	util.Assertf(a.vid.GetTxStatus() == vertex.Good, "a.vid.GetTxStatus() == vertex.Good")
	util.Assertf(len(a.undefinedPastVertices) == 0, "len(a.undefinedPastVertices)==0")
	util.Assertf(len(a.pendingOutputs) == 0, "len(a.pendingOutputs)==0")
	util.Assertf(len(a.rooted) > 0, "len(a.rooted) > 0")
	util.Assertf(len(a.goodPastVertices) > 0, "len(a.goodPastVertices) > 0")

	if a.vid.IsBranchTransaction() {
		coverage := a.commitBranch()
		a.vid.SetLedgerCoverage(coverage)
		a.env.AddBranch(a.vid)
		a.env.EvidenceBookedBranch(a.vid.ID(), a.vid.MustSequencerID())
	} else {
		coverage := a.calculateCoverage()
		a.vid.SetLedgerCoverage(coverage)
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
	for vid := range a.goodPastVertices {
		muts.InsertAddTxMutation(*vid.ID(), a.vid.TimeSlot())

		// ADD OUTPUT mutations only for not consumed outputs
		producedOutputIndices := vid.NotConsumedOutputIndices(a.goodPastVertices)
		for _, idx := range producedOutputIndices {
			muts.InsertAddOutputMutation(vid.OutputID(idx), vid.MustOutputAt(idx))
		}
	}

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
	return prevCoverage.MakeNext(int(a.vid.TimeSlot())-int(a.baselineBranch.TimeSlot()), coverageDelta)
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
