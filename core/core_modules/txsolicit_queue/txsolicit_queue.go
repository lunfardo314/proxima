// Package txsolicit_queue is the fast-track input queue for solicited (wanted/pulled) transactions.
// Transactions enter here from txstore lookups or peer pull responses.
// No dedup, no rate control, no gossip — all transactions are attached directly.
package txsolicit_queue

import (
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

const Name = "txSolicitQueue"

type (
	environment interface {
		global.NodeGlobal
	}

	AttachFun func(tx *transaction.Transaction, opts ...attacher.AttachTxOption)

	Input struct {
		// either TxBytesWithMetadata or Tx is set
		TxBytesWithMetadata []byte                   // raw bytes from txstore (includes metadata)
		Tx                  *transaction.Transaction // already parsed transaction
	}

	TxSolicitQueue struct {
		environment
		*core_modules.CoreModule[*Input]
		attachFun AttachFun
	}
)

func New(env environment, attachFun AttachFun) *TxSolicitQueue {
	ret := &TxSolicitQueue{
		environment: env,
		attachFun:   attachFun,
	}
	ret.CoreModule = core_modules.New(env, Name, ret.consume)
	ret.CoreModule.Start()
	return ret
}

func (q *TxSolicitQueue) consume(inp *Input) {
	var tx *transaction.Transaction

	if inp.Tx != nil {
		tx = inp.Tx
	} else {
		// parse raw bytes from txstore
		txBytes, _, err := txmetadata.ParseTxMetadata(inp.TxBytesWithMetadata)
		if err != nil {
			q.Log().Warnf("%s: failed to parse txstore bytes: %v", Name, err)
			return
		}
		tx, err = transaction.Parse(txBytes)
		if err != nil {
			q.Log().Warnf("%s: failed to parse transaction: %v", Name, err)
			return
		}
	}

	// reparsed transactions need partial context validation (initializes internal structures)
	// validation script is skipped because it is assumed that partial context (including signature) was already validated once
	if err := tx.ValidatePartialContext(false); err != nil {
		q.Log().Warnf("%s: partial context validation failed for %s: %v", Name, tx.IDShortString(), err)
		return
	}

	nowis := time.Now()
	meta := txmetadata.TransactionMetadata{
		SourceTypeNonPersistent: txmetadata.SourceTypeTxStore,
		TxBytesReceived:         &nowis,
	}

	// wait for clock to catch up if the transaction is slightly in the future
	if !q.ClockCatchUpWithLedgerTime(tx.Timestamp()) {
		return // interrupted by shutdown
	}

	q.attachFun(tx,
		attacher.WithTransactionMetadata(&meta),
		attacher.WithInvokedBy("txSolicit"),
	)
}

func (q *TxSolicitQueue) PushTxBytesFromStore(txBytesWithMetadata []byte) {
	q.Push(&Input{TxBytesWithMetadata: txBytesWithMetadata})
}

// PushParsedTx pushes an already-parsed transaction (e.g. from attacher txstore lookup).
func (q *TxSolicitQueue) PushParsedTx(tx *transaction.Transaction) {
	q.Push(&Input{Tx: tx})
}

// TxBytesFromStoreIn is the replacement for workflow.TxBytesFromStoreIn.
// It parses txstore bytes, extracts txid, and queues the transaction.
func (q *TxSolicitQueue) TxBytesFromStoreIn(txBytesWithMetadata []byte) (base.TransactionID, error) {
	txBytes, _, err := txmetadata.ParseTxMetadata(txBytesWithMetadata)
	if err != nil {
		return base.TransactionID{}, err
	}
	tx, err := transaction.Parse(txBytes)
	if err != nil {
		return base.TransactionID{}, err
	}
	q.Push(&Input{Tx: tx})
	return tx.ID(), nil
}
