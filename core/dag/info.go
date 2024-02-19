package dag

import (
	"bytes"
	"sort"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
)

func (d *DAG) Info(verbose ...bool) string {
	return d.InfoLines(verbose...).String()
}

func (d *DAG) InfoLines(verbose ...bool) *lines.Lines {
	ln := lines.New()

	slots := d._timeSlotsOrdered()

	ln.Add("DAG:: vertices: %d, stateReaders: %d, slots: %d",
		len(d.vertices), len(d.stateReaders), len(slots))

	if len(verbose) > 0 && verbose[0] {
		vertices := d.Vertices()
		ln.Add("---- all vertices (verbose)")
		sort.Slice(vertices, func(i, j int) bool {
			return ledger.LessTxID(vertices[i].ID, vertices[j].ID)
		})
		for _, vid := range vertices {
			ln.Add("    " + vid.ShortString())
		}

		ln.Add("---- cached state readers (verbose)")
		func() {
			d.stateReadersMutex.Lock()
			defer d.stateReadersMutex.Unlock()

			branches := util.KeysSorted(d.stateReaders, func(id1, id2 ledger.TransactionID) bool {
				return bytes.Compare(id1[:], id2[:]) < 0
			})
			for _, br := range branches {
				rdrData := d.stateReaders[br]
				ln.Add("    %s, last activity %v", br.StringShort(), time.Since(rdrData.lastActivity))
			}
		}()
	}
	return ln
}

func (d *DAG) VerticesInSlotAndAfter(slot ledger.Slot) []*vertex.WrappedTx {
	ret := d.Vertices(func(txid *ledger.TransactionID) bool {
		return txid.Slot() >= slot
	})
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
	for br := range d.stateReaders {
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
	return multistate.FetchSummarySupply(d.StateStore(), nBack)
}

//
//func (ut *DAG) MustAccountInfoOfHeaviestBranch() *multistate.AccountInfo {
//	return multistate.MustCollectAccountInfo(ut.stateStore, ut.HeaviestStateRootForLatestTimeSlot())
//}
