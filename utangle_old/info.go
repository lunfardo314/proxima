package utangle_old

import (
	"bytes"

	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type ()

func (ut *UTXOTangle) NumVertices() int {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	return len(ut.vertices)
}

func (ut *UTXOTangle) Info(verbose ...bool) string {
	return ut.InfoLines(verbose...).String()
}

func (ut *UTXOTangle) InfoLines(verbose ...bool) *lines.Lines {
	ut.mutex.RLock()
	defer ut.mutex.RUnlock()

	ln := lines.New()
	slots := ut._timeSlotsOrdered()

	verb := false
	if len(verbose) > 0 {
		verb = verbose[0]
	}
	ln.Add("UTXOTangle (verbose = %v), numVertices: %d, num slots: %d, addTx: %d, delTx: %d, addBranch: %d, delBranch: %d",
		verb, ut.NumVertices(), len(slots), ut.numAddedVertices, ut.numDeletedVertices, ut.numAddedBranches, ut.numDeletedBranches)
	for _, e := range slots {
		branches := util.SortKeys(ut.branches[e], func(vid1, vid2 *WrappedTx) bool {
			chainID1, ok := vid1.SequencerIDIfAvailable()
			util.Assertf(ok, "can't gets sequencer ID")
			chainID2, ok := vid2.SequencerIDIfAvailable()
			util.Assertf(ok, "can't gets sequencer ID")
			return bytes.Compare(chainID1[:], chainID2[:]) < 0
		})

		ln.Add("---- slot %8d : branches: %d", e, len(branches))
		for _, vid := range branches {
			coverage := ut.LedgerCoverage(vid)
			seqID, isAvailable := vid.SequencerIDIfAvailable()
			util.Assertf(isAvailable, "sequencer ID expected in %s", vid.IDShort())
			ln.Add("    branch %s, seqID: %s, coverage: %s", vid.IDShort(), seqID.Short(), util.GoThousands(coverage))
			if verb {
				ln.Add("    == root: " + ut.branches[e][vid].String()).
					Append(vid.Lines("    "))
			}
		}
	}
	return ln
}

func (ut *UTXOTangle) FetchSummarySupplyAndInflation(nBack int) *multistate.SummarySupplyAndInflation {
	return multistate.FetchSummarySupplyAndInflation(ut.stateStore, nBack)
}

func (ut *UTXOTangle) MustAccountInfoOfHeaviestBranch() *multistate.AccountInfo {
	return multistate.MustCollectAccountInfo(ut.stateStore, ut.HeaviestStateRootForLatestTimeSlot())
}
