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
	vidBranch := w.MustEnsureBranch(branchData.Stem.ID.TransactionID())
	w.PostEventNewGood(vidBranch)
	loadedTxs.Insert(vidBranch)

	// load sequencer output for the chain
	seqOut, stemOut, err := rdr.GetSequencerOutputs(&seqID)
	if err != nil {
		return fmt.Errorf("LoadSequencerTips: %w", err)
	}
	wOut, _, err := attacher.AttachSequencerOutputs(seqOut, stemOut, w, attacher.OptionInvokedBy("LoadSequencerTips"))
	if err != nil {
		return err
	}
	loadedTxs.Insert(wOut.VID)

	w.Log().Infof("loaded starting output for sequencer %s: %s. From branch %s", seqID.StringShort(), seqOut.ID.StringShort(), vidBranch.IDShortString())

	// load pending tag-along outputs
	oids, err := rdr.GetIDsLockedInAccount(seqID.AsChainLock().AccountID())
	util.AssertNoError(err)
	for _, oid := range oids {
		o := rdr.MustGetOutputWithID(&oid)
		wOut, err = attacher.AttachOutputWithID(o, w, attacher.OptionInvokedBy("LoadSequencerTips"))
		if err != nil {
			return err
		}
		w.Log().Infof("loaded tag-along input for sequencer %s: %s from branch %s", seqID.StringShort(), oid.StringShort(), vidBranch.IDShortString())
		loadedTxs.Insert(wOut.VID)
	}
	// post new tx event for each transaction
	for vid := range loadedTxs {
		w.PostEventNewTransaction(vid)
	}
	return nil
}
