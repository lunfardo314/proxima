package workflow

import (
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

// ListenToAccount listens to all unlockable with account ID
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

const fetchLastNTimeSlotsUponStartup = 5

func (w *Workflow) ScanAccount(addr ledger.AccountID) set.Set[vertex.WrappedOutput] {
	roots := multistate.FetchRootRecordsNSlotsBack(w.StateStore(), fetchLastNTimeSlotsUponStartup)

	ret := set.New[vertex.WrappedOutput]()
	for _, rr := range roots {
		rdr := multistate.MustNewSugaredReadableState(w.StateStore(), rr.Root, 0)
		oids, err := rdr.GetIDsLockedInAccount(addr)
		util.AssertNoError(err)
		for _, oid := range oids {
			ret.Insert(attacher.AttachOutputID(oid, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("ScanAccount")))
		}
	}
	// TODO scan utangle?
	return ret
}
