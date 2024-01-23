package dag

import (
	"sort"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/maps"
)

type ()

func (d *DAG) NumVertices() int {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return len(d.vertices)
}

func (d *DAG) Info(verbose ...bool) string {
	return d.InfoLines(verbose...).String()
}

func (d *DAG) InfoLines(verbose ...bool) *lines.Lines {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	ln := lines.New()

	slots := d._timeSlotsOrdered()

	ln.Add("DAG:: vertices: %d, branches: %d, slots: %d",
		len(d.vertices), len(d.branches), len(slots))
	if len(verbose) > 0 && verbose[0] {
		ln.Add("---- all vertices (verbose)")
		all := maps.Values(d.vertices)
		sort.Slice(all, func(i, j int) bool {
			return ledger.LessTxID(all[i].ID, all[j].ID)
		})
		for _, vid := range all {
			ln.Add("    " + vid.ShortString())
		}
	}

	branches := util.SortKeys(d.branches, func(vid1, vid2 *vertex.WrappedTx) bool {
		return vid1.Slot() > vid2.Slot()
	})

	ln.Add("---- branches")
	for _, vidBranch := range branches {
		ln.Add("    %s : coverage %s", vidBranch.IDShortString(), vidBranch.GetLedgerCoverage().String())
	}
	return ln
}

func (d *DAG) VerticesInSlotAndAfter(slot ledger.Slot) []*vertex.WrappedTx {
	ret := make([]*vertex.WrappedTx, 0)

	d.mutex.RLock()
	defer d.mutex.RUnlock()

	for _, vid := range d.vertices {
		if vid.Slot() >= slot {
			ret = append(ret, vid)
		}
	}
	sort.Slice(ret, func(i, j int) bool {
		return vertex.LessByCoverageAndID(ret[i], ret[j])
	})
	return ret
}

func (d *DAG) LinesVerticesInSlotAndAfter(slot ledger.Slot) *lines.Lines {
	return vertex.VerticesLines(d.VerticesInSlotAndAfter(slot))
}

func (d *DAG) _timeSlotsOrdered(descOrder ...bool) []ledger.Slot {
	desc := false
	if len(descOrder) > 0 {
		desc = descOrder[0]
	}
	slots := set.New[ledger.Slot]()
	for br := range d.branches {
		slots.Insert(br.Slot())
	}
	if desc {
		return util.SortKeys(slots, func(e1, e2 ledger.Slot) bool {
			return e1 > e2
		})
	}
	return util.SortKeys(slots, func(e1, e2 ledger.Slot) bool {
		return e1 < e2
	})
}

func (d *DAG) FetchSummarySupplyAndInflation(nBack int) *multistate.SummarySupplyAndInflation {
	return multistate.FetchSummarySupplyAndInflation(d.stateStore, nBack)
}

//
//func (ut *DAG) MustAccountInfoOfHeaviestBranch() *multistate.AccountInfo {
//	return multistate.MustCollectAccountInfo(ut.stateStore, ut.HeaviestStateRootForLatestTimeSlot())
//}
