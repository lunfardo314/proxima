package workflow

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules/txinput_queue"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

func (w *Workflow) TxFromStoreIn(txid base.TransactionID) (err error) {
	_, err = w.TxBytesFromStoreIn(w.TxBytesStore().GetTxBytesWithMetadata(&txid))
	return
}

func (w *Workflow) TxBytesFromStoreIn(txBytesWithMetadata []byte) (base.TransactionID, error) {
	return w.txSolicitQueue.TxBytesFromStoreIn(txBytesWithMetadata)
}

// TxBytesInForTests parses and processes a transaction synchronously.
// Used in tests and for direct submission (not through gossip/API queues).
func (w *Workflow) TxBytesInForTests(txBytes []byte) (base.TransactionID, error) {
	tx, err := transaction.Parse(txBytes)
	if err != nil {
		return base.TransactionID{}, err
	}
	if err = tx.ValidatePartialContext(true); err != nil {
		return base.TransactionID{}, err
	}
	nowis := time.Now()
	meta := &txmetadata.TransactionMetadata{
		SourceTypeNonPersistent: txmetadata.SourceTypeAPI,
		TxBytesReceived:         &nowis,
	}
	w.MustPersistTxBytesWithMetadata(tx, meta)

	opts := []attacher.AttachTxOption{
		attacher.WithTransactionMetadata(meta),
		attacher.WithInvokedBy("TxBytesInForTests"),
		attacher.WithEnforceTimestampBeforeRealTime,
	}
	txid := tx.ID()
	txTime := ledger.ClockTime(txid.Timestamp())
	if time.Until(txTime) <= 0 {
		w._attach(tx, opts...)
	} else {
		go func() {
			if !w.ClockCatchUpWithLedgerTime(txid.Timestamp()) {
				return
			}
			w._attach(tx, opts...)
		}()
	}
	return txid, nil
}

func (w *Workflow) TxBytesInFromAPIQueued(txBytes []byte) {
	w.txInputQueue.Push(txinput_queue.Input{
		Cmd:     txinput_queue.CmdFromAPI,
		TxBytes: txBytes,
		TxMetaData: &txmetadata.TransactionMetadata{
			SourceTypeNonPersistent: txmetadata.SourceTypeAPI,
			TxBytesReceived:         util.Ref(time.Now()),
		},
	})
}

func (w *Workflow) TxBytesInFromPeerQueued(txBytesReceived []byte, metaData *txmetadata.TransactionMetadata, from peer.ID, txidPrefix base.TransactionID) {
	if metaData == nil {
		metaData = &txmetadata.TransactionMetadata{}
	}
	metaData.TxBytesReceived = util.Ref(time.Now())
	w.txInputQueue.Push(txinput_queue.Input{
		Cmd:        txinput_queue.CmdFromPeer,
		PrefixTxID: txidPrefix,
		TxBytes:    txBytesReceived,
		TxMetaData: metaData,
		FromPeer:   from,
	})
}

func (w *Workflow) _attach(tx *transaction.Transaction, opts ...attacher.AttachTxOption) {
	attacher.AttachTransaction(tx, w, opts...)
}

func (w *Workflow) OwnSequencerMilestoneIn(txBytes []byte, meta *txmetadata.TransactionMetadata, txid base.TransactionID) {
	w.TxBytesInFromPeerQueued(txBytes, meta, w.SelfPeerID(), txid)
}
