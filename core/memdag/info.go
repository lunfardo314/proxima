package memdag

import (
	"sort"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

func (d *MemDAG) Info(verbose ...bool) string {
	return d.InfoLines(verbose...).String()
}

func (d *MemDAG) InfoLines(verbose ...bool) *lines.Lines {
	ln := lines.New()

	d.mutex.RLock()
	ln.Add("MemDAG:: vertices: %d", len(d.vertices))
	d.mutex.RUnlock()

	if len(verbose) > 0 && verbose[0] {
		verticesWithFlags := d.VerticesWitExpirationFlag()
		ln.Add("---- all vertices (verbose)")
		vertices := util.KeysSorted(verticesWithFlags, func(vid1, vid2 *vertex.WrappedTx) bool {
			return vid1.SlotWhenAdded < vid2.SlotWhenAdded
		})
		for _, vid := range vertices {
			ln.Add("    %s -- expired: %v", vid.ShortString(), verticesWithFlags[vid])
		}
	}
	return ln
}

func (d *MemDAG) VerticesInSlotAndAfter(slot uint32) []*vertex.WrappedTx {
	ret := d.VerticesFiltered(func(txid base.TransactionID) bool {
		return txid.Slot() >= slot
	})
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (d *MemDAG) LinesVerticesInSlotAndAfter(slot uint32) *lines.Lines {
	return vertex.VerticesLines(d.VerticesInSlotAndAfter(slot))
}

func (d *MemDAG) FetchSummarySupplyAndInflation(nBack int) *multistate.SummarySupplyAndInflation {
	return multistate.FetchSummarySupply(d.StateStore(), nBack)
}
