package workflow

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

// ListenToAccount listens to all unlockable with account ID
func (w *Workflow) ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput)) {
	w.events.OnEvent(EventNewTx, func(vid *vertex.WrappedTx) {
		var _indices [256]byte
		indices := _indices[:0]
		vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
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
		// only sequencer tx can become 'good'
		fun(vid)
	})
}

const fetchLastNTimeSlotsUponStartup = 5

func (w *Workflow) PullSequencerTips(seqID ledger.ChainID, loadOwnMilestones bool) (set.Set[vertex.WrappedOutput], error) {
	roots := multistate.FetchRootRecordsNSlotsBack(w.StateStore(), fetchLastNTimeSlotsUponStartup)

	addr := seqID.AsChainLock().AccountID()
	ret := set.New[vertex.WrappedOutput]()
	ownMilestoneLoaded := false
	for _, rr := range roots {
		rdr := multistate.MustNewSugaredReadableState(w.StateStore(), rr.Root, 0)
		// load each branch
		vidBranch := attacher.MustEnsureBranch(rdr.GetStemOutput().ID.TransactionID(), w, 0)
		w.PostEventNewGood(vidBranch)

		if loadOwnMilestones {
			// load sequencer output in each of those branches
			if oSeq, _ := rdr.GetUTXOForChainID(&seqID); oSeq != nil {
				attacher.AttachOutputID(oSeq.ID, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("PullSequencerTips"))
				ownMilestoneLoaded = true
			}
		}
		// load all tag along outputs
		oids, err := rdr.GetIDsLockedInAccount(addr)
		util.AssertNoError(err)
		for _, oid := range oids {
			ret.Insert(attacher.AttachOutputID(oid, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("PullSequencerTips")))
		}
	}
	if loadOwnMilestones && !ownMilestoneLoaded {
		return nil, fmt.Errorf("PullSequencerTips: failed to load milestone for the sequencer %s", seqID.StringShort())
	}

	// scan all vertices and post new tx event
	w.ForEachVertex(func(vid *vertex.WrappedTx) bool {
		vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			w.PostEventNewTransaction(vid)
		}})
		return true
	})
	return ret, nil
}
