package dag

import (
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type ()

func (ut *DAG) NumVertices() int {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return len(ut.vertices)
}

func (ut *DAG) Info() string {
	return ut.InfoLines().String()
}

func (ut *DAG) InfoLines() *lines.Lines {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	ln := lines.New()
	slots := ut._timeSlotsOrdered()

	ln.Add("DAG:: numVertices: %d, num slots: %d, addTx: %d, delTx: %d, addBranch: %d, delBranch: %d",
		ut.NumVertices(), len(slots), ut.numAddedVertices, ut.numDeletedVertices, ut.numAddedBranches, ut.numDeletedBranches)
	branches := util.SortKeys(ut.branches, func(vid1, vid2 *vertex.WrappedTx) bool {
		return vid1.TimeSlot() > vid2.TimeSlot()
	})

	for _, vidBranch := range branches {
		ln.Add("    %s : coverage %s", vidBranch.IDShortString(), vidBranch.GetLedgerCoverage().String())
	}
	return ln
}

func (ut *DAG) FetchSummarySupplyAndInflation(nBack int) *multistate.SummarySupplyAndInflation {
	return multistate.FetchSummarySupplyAndInflation(ut.stateStore, nBack)
}

//
//func (ut *DAG) MustAccountInfoOfHeaviestBranch() *multistate.AccountInfo {
//	return multistate.MustCollectAccountInfo(ut.stateStore, ut.HeaviestStateRootForLatestTimeSlot())
//}
