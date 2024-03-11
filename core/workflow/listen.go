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

// ListenToAccount listens to all outputs unlockable with account ID (except stem-locked outputs)
func (w *Workflow) ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput)) {
	w.events.OnEvent(EventNewTx, func(vid *vertex.WrappedTx) {
		var _indices [256]byte
		indices := _indices[:0]
		vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			v.Tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, _ *ledger.OutputID) bool {
				if o.Lock().UnlockableWith(account.AccountID()) && o.Lock().Name() != ledger.StemLockName {
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

// LoadSequencerTips implements interface
func (w *Workflow) LoadSequencerTips(seqID ledger.ChainID) error {
	roots := multistate.FetchRootRecordsNSlotsBack(w.StateStore(), fetchLastNTimeSlotsUponStartup)

	w.Log().Infof("loading tips for sequencer %s from %d branches", seqID.StringShort(), len(roots))

	addr := seqID.AsChainLock().AccountID()
	loadedTxs := set.New[*vertex.WrappedTx]()
	ownMilestoneLoaded := false
	for _, rr := range roots {
		rdr := multistate.MustNewSugaredReadableState(w.StateStore(), rr.Root, 0)
		// load each branch
		vidBranch := attacher.MustEnsureBranch(rdr.GetStemOutput().ID.TransactionID(), w, 0)
		w.PostEventNewGood(vidBranch)
		loadedTxs.Insert(vidBranch)

		// load sequencer output in each of those branches
		if oSeq, _ := rdr.GetUTXOForChainID(&seqID); oSeq != nil {
			w.Log().Infof("loading sequencer output for %s: %s from branch %s", seqID.StringShort(), oSeq.ID.StringShort(), vidBranch.IDShortString())
			wOut := attacher.AttachOutputID(oSeq.ID, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("LoadSequencerTips"))
			loadedTxs.Insert(wOut.VID)
			ownMilestoneLoaded = true
		}
		// load all tag-along outputs (i.e. chain-locked in the seqID)
		oids, err := rdr.GetIDsLockedInAccount(addr)
		util.AssertNoError(err)
		for _, oid := range oids {
			w.Log().Infof("loading tag-along input for sequencer %s: %s from branch %s", seqID.StringShort(), oid.StringShort(), vidBranch.IDShortString())
			wOut := attacher.AttachOutputID(oid, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("LoadSequencerTips"))
			loadedTxs.Insert(wOut.VID)
		}
	}
	if !ownMilestoneLoaded {
		return fmt.Errorf("LoadSequencerTips: unable to load any milestone for the sequencer %s", seqID.StringShort())
	}

	// post new tx event for each transaction
	for vid := range loadedTxs {
		vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			w.PostEventNewTransaction(vid)
		}})
	}
	return nil
}
