package workflow

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func (w *Workflow) ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput)) {
	w.events.OnEvent(EventNewValidatedTx, func(vid *vertex.WrappedTx) {
		var _indices [256]byte
		indices := _indices[:0]
		vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			util.Assertf(v.FlagsUp(vertex.FlagConstraintsValid), "v.FlagsUp(vertex.FlagConstraintsValid)")
			v.Tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, _ *ledger.OutputID) bool {
				if o.Lock().UnlockableWith(account.AccountID()) {
					indices = append(indices, idx)
				}
				return true
			})
		}})
		for _, idx := range indices {
			fun(vertex.WrappedOutput{
				VID:   vid,
				Index: idx,
			})
		}
	})
}

func (w *Workflow) ListenToSequencers(fun func(vid *vertex.WrappedTx)) {
	w.events.OnEvent(EventNewGoodTx, func(vid *vertex.WrappedTx) {
		fun(vid)
	})
}

func (w *Workflow) ScanAccount(addr ledger.AccountID) set.Set[vertex.WrappedOutput] {
	heaviest := util.Maximum(multistate.FetchRootRecordsNSlotsBack(w.StateStore(), 1), func(rr1, rr2 multistate.RootRecord) bool {
		return rr1.LedgerCoverage.Sum() > rr2.LedgerCoverage.Sum()
	})
	rdr := multistate.MustNewSugaredReadableState(w.StateStore(), heaviest.Root)
	oids, err := rdr.GetIDsLockedInAccount(addr)
	util.AssertNoError(err)

	ret := set.New[vertex.WrappedOutput]()
	for _, oid := range oids {
		ret.Insert(attacher.AttachOutputID(oid, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("ScanAccount")))
	}
	// TODO scan utangle
	return ret
}
