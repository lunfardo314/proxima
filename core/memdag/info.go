package memdag

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

func (d *MemDAG) Info(verbose ...bool) string {
	return d.InfoLines(verbose...).String()
}

func (d *MemDAG) InfoLines(verbose ...bool) *lines.Lines {
	ln := lines.New()

	slots := d._timeSlotsOrdered()

	ln.Add("MemDAG:: vertices: %d, stateReaders: %d, slots: %d",
		len(d.vertices), len(d.stateReaders), len(slots))

	if len(verbose) > 0 && verbose[0] {
		vertices := d.Vertices()
		ln.Add("---- all vertices (verbose)")
		sort.Slice(vertices, func(i, j int) bool {
			return ledger.LessTxID(vertices[i].ID, vertices[j].ID)
		})
		for _, vid := range vertices {
			ln.Add("    %s, referenced by: %d", vid.ShortString(), vid.NumReferences())
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

func (d *MemDAG) VerticesInSlotAndAfter(slot ledger.Slot) []*vertex.WrappedTx {
	ret := d.Vertices(func(txid *ledger.TransactionID) bool {
		return txid.Slot() >= slot
	})
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (d *MemDAG) LinesVerticesInSlotAndAfter(slot ledger.Slot) *lines.Lines {
	return vertex.VerticesLines(d.VerticesInSlotAndAfter(slot))
}

func (d *MemDAG) _timeSlotsOrdered(descOrder ...bool) []ledger.Slot {
	desc := false
	if len(descOrder) > 0 {
		desc = descOrder[0]
	}
	slots := set.New[ledger.Slot]()
	for br := range d.stateReaders {
		slots.Insert(br.Slot())
	}
	if desc {
		return util.KeysSorted(slots, func(e1, e2 ledger.Slot) bool {
			return e1 > e2
		})
	}
	return util.KeysSorted(slots, func(e1, e2 ledger.Slot) bool {
		return e1 < e2
	})
}

func (d *MemDAG) FetchSummarySupplyAndInflation(nBack int) *multistate.SummarySupplyAndInflation {
	return multistate.FetchSummarySupply(d.StateStore(), nBack)
}

//
//func (ut *MemDAG) MustAccountInfoOfHeaviestBranch() *multistate.AccountInfo {
//	return multistate.MustCollectAccountInfo(ut.stateStore, ut.HeaviestStateRootForLatestTimeSlot())
//}
