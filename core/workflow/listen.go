package workflow

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

// ListenToAccount listens to all outputs which belongs to the account (except stem-locked outputs)
func (w *Workflow) ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput)) {
	w.events.OnEvent(EventNewTx, func(vid *vertex.WrappedTx) {
		var _indices [256]byte
		indices := _indices[:0]
		vid.RUnwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			v.Tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, _ *ledger.OutputID) bool {
				if ledger.BelongsToAccount(o.Lock(), account) && o.Lock().Name() != ledger.StemLockName {
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

// LoadSequencerTips loads tip transactions relevant to the sequencer startup from persistent state to the memDAG
// The only chain output is loaded from the LRB
func (w *Workflow) LoadSequencerTips(seqID ledger.ChainID) error {
	branchData, found := multistate.FindLatestReliableBranch(w.StateStore(), global.FractionHealthyBranch)
	if !found {
		return fmt.Errorf("LoadSequencerTips: can't find latest reliable branch (LRB) with franction %s", global.FractionHealthyBranch.String())
	}
	loadedTxs := set.New[*vertex.WrappedTx]()
	nowSlot := ledger.TimeNow().Slot()
	w.Log().Infof("loading sequencer tips for %s from LRB %s, %d slots back from (current slot is %d)",
		seqID.StringShort(), branchData.TxID().StringShort(), nowSlot-branchData.TxID().Slot(), nowSlot)

	rdr := multistate.MustNewSugaredReadableState(w.StateStore(), branchData.Root, 0)
	vidBranch := attacher.MustEnsureBranch(branchData.Stem.ID.TransactionID(), w, 0)
	w.PostEventNewGood(vidBranch)
	loadedTxs.Insert(vidBranch)

	// load chain output
	oSeq, _ := rdr.GetUTXOForChainID(&seqID)
	if oSeq == nil {
		return fmt.Errorf("LoadSequencerTips: unable to load any milestone for the sequencer %s", seqID.StringShort())
	}
	w.Log().Infof("loaded sequencer output %s for %s:\n%s",
		oSeq.ID.StringShort(), seqID.String(), oSeq.MustParse().Output.Lines("        ").String())
	wOut := attacher.AttachOutputID(oSeq.ID, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("LoadSequencerTips"))
	loadedTxs.Insert(wOut.VID)

	// load pending tag-along outputs
	oids, err := rdr.GetIDsLockedInAccount(seqID.AsChainLock().AccountID())
	util.AssertNoError(err)
	for _, oid := range oids {
		w.Log().Infof("loading tag-along input for sequencer %s: %s from branch %s", seqID.StringShort(), oid.StringShort(), vidBranch.IDShortString())
		wOut := attacher.AttachOutputID(oid, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("LoadSequencerTips"))
		loadedTxs.Insert(wOut.VID)
	}
	// post new tx event for each transaction
	for vid := range loadedTxs {
		w.PostEventNewTransaction(vid)
	}
	return nil
}

//const fetchLastNTimeSlotsUponStartup = 5
// LoadSequencerTipsOld pulls tip transactions relevant to the sequencer startup from fixed amount of latest slots
//func (w *Workflow) LoadSequencerTipsOld(seqID ledger.ChainID) error {
//	roots := multistate.FetchRootRecordsNSlotsBack(w.StateStore(), fetchLastNTimeSlotsUponStartup)
//
//	w.Log().Infof("loading tips for sequencer %s from %d branches", seqID.StringShort(), len(roots))
//
//	addr := seqID.AsChainLock().AccountID()
//	loadedTxs := set.New[*vertex.WrappedTx]()
//	ownMilestoneLoaded := false
//	for _, rr := range roots {
//		rdr := multistate.MustNewSugaredReadableState(w.StateStore(), rr.Root, 0)
//		// load each branch
//		vidBranch := attacher.MustEnsureBranch(rdr.GetStemOutput().ID.TransactionID(), w, 0)
//		w.PostEventNewGood(vidBranch)
//		loadedTxs.Insert(vidBranch)
//
//		// load sequencer output in each of those branches
//		if oSeq, _ := rdr.GetUTXOForChainID(&seqID); oSeq != nil {
//			w.Log().Infof("loading sequencer output for %s: %s from branch %s", seqID.StringShort(), oSeq.ID.StringShort(), vidBranch.IDShortString())
//			wOut := attacher.AttachOutputID(oSeq.ID, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("LoadSequencerTips"))
//			loadedTxs.Insert(wOut.VID)
//			ownMilestoneLoaded = true
//		}
//		// load all tag-along outputs (i.e. chain-locked in the seqID)
//		oids, err := rdr.GetIDsLockedInAccount(addr)
//		util.AssertNoError(err)
//		for _, oid := range oids {
//			w.Log().Infof("loading tag-along input for sequencer %s: %s from branch %s", seqID.StringShort(), oid.StringShort(), vidBranch.IDShortString())
//			wOut := attacher.AttachOutputID(oid, w, attacher.OptionPullNonBranch, attacher.OptionInvokedBy("LoadSequencerTips"))
//			loadedTxs.Insert(wOut.VID)
//		}
//	}
//	if !ownMilestoneLoaded {
//		// at least 1 branch must contain output with sequencer chain
//		return fmt.Errorf("LoadSequencerTips: unable to load any milestone for the sequencer %s", seqID.StringShort())
//	}
//	// post new tx event for each transaction
//	for vid := range loadedTxs {
//		w.PostEventNewTransaction(vid)
//	}
//	return nil
//}
