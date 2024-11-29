package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
)

// TODO revisit MaxDurationInTheFuture

func (w *Workflow) MaxDurationInTheFuture() time.Duration {
	return 10 * ledger.SlotDuration()
}

func (w *Workflow) PokeMe(me, with *vertex.WrappedTx) {
	w.poker.PokeMe(me, with)
}

func (w *Workflow) PokeAllWith(wanted *vertex.WrappedTx) {
	w.poker.PokeAllWith(wanted)
}

func (w *Workflow) SendTxBytesWithMetadataToPeer(id peer.ID, txBytes []byte, metadata *txmetadata.TransactionMetadata) bool {
	return w.peers.SendTxBytesWithMetadataToPeer(id, txBytes, metadata)
}

func (w *Workflow) GossipAttachedTransaction(tx *transaction.Transaction, metadata *txmetadata.TransactionMetadata) {
	if metadata != nil {
		if metadata.SourceTypeNonPersistent == txmetadata.SourceTypeTxStore || metadata.SourceTypeNonPersistent == txmetadata.SourceTypePulled {
			return
		}
	}
	w.GossipTxBytesToPeers(tx.Bytes(), metadata)
}

func (w *Workflow) GossipTxBytesToPeers(txBytes []byte, metadata *txmetadata.TransactionMetadata, except ...peer.ID) {
	w.peers.GossipTxBytesToPeers(txBytes, metadata, except...)
}

func (w *Workflow) MustPersistTxBytesWithMetadata(txBytes []byte, metadata *txmetadata.TransactionMetadata) {
	_, err := w.TxBytesStore().PersistTxBytesWithMetadata(txBytes, metadata)
	util.AssertNoError(err)
}

func (w *Workflow) SendToTippool(vid *vertex.WrappedTx) {
	w.tippool.Push(tippool.Input{VID: vid})
}

func (w *Workflow) IsSynced() bool {
	slotNow := ledger.TimeNow().Slot()
	return slotNow == 0 || multistate.FirstHealthySlotIsNotBefore(w.StateStore(), slotNow-1, global.FractionHealthyBranch)
}

// LatestMilestonesDescending returns optionally filtered sorted transactions from the sequencer tippool
func (w *Workflow) LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	return w.tippool.LatestMilestonesDescending(filter...)
}

// LatestMilestonesShuffled returns optionally filtered sorted transactions from the sequencer tippool
func (w *Workflow) LatestMilestonesShuffled(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	return w.tippool.LatestMilestonesShuffled(filter...)
}

func (w *Workflow) GetLatestMilestone(seqID ledger.ChainID) *vertex.WrappedTx {
	return w.tippool.GetLatestMilestone(seqID)
}

func (w *Workflow) NumSequencerTips() int {
	return w.tippool.NumSequencerTips()
}

func (w *Workflow) PeerName(id peer.ID) string {
	return w.peers.PeerName(id)
}

func (w *Workflow) QueryTxIDStatus(txid *ledger.TransactionID) (ret vertex.TxIDStatus) {
	ret = w.MemDAG.QueryTxIDStatus(txid)
	ret.InStorage = w.TxBytesStore().HasTxBytes(txid)
	return
}

func (w *Workflow) QueryTxIDStatusJSONAble(txid *ledger.TransactionID) vertex.TxIDStatusJSONAble {
	ret := w.QueryTxIDStatus(txid)
	return ret.JSONAble()
}

func (w *Workflow) GetTxInclusion(txid *ledger.TransactionID, slotsBack int) *multistate.TxInclusion {
	return multistate.GetTxInclusion(w.StateStore(), txid, slotsBack)
}

func (w *Workflow) WaitTxIDDefined(txid *ledger.TransactionID, pollPeriod, timeout time.Duration) (vertex.Status, error) {
	deadline := time.Now().Add(timeout)
	for {
		status := w.QueryTxIDStatus(txid)
		if status.Status != vertex.Undefined {
			return status.Status, nil
		}
		time.Sleep(pollPeriod)
		if time.Now().After(deadline) {
			return vertex.Undefined, fmt.Errorf("timeout")
		}
	}
}

func (w *Workflow) AddWantedTransaction(txid *ledger.TransactionID) {
	w.txInputQueue.AddWantedTransaction(txid)
}

func (w *Workflow) EvidenceNonSequencerTx() {
	w.txInputQueue.EvidenceNonSequencerTx()
}

func (w *Workflow) SaveFullDAG(fname string) {
	branchTxIDS := multistate.FetchLatestBranchTransactionIDs(w.StateStore())
	tmpDag := memdag.MakeDAGFromTxStore(w.TxBytesStore(), 0, branchTxIDS...)
	tmpDag.SaveGraph(fname)
}
