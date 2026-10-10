package glb

import (
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// Wallet-side foundation helpers for the wasm-style refactor. See
// kb/archive/shipped/proxi_txbuildercore.md (Phase 0.3) for context. Tx
// construction sites use GetTxLibrary + SubmitAndDisplay; they do
// NOT touch the ledger.L() singleton.

var (
	txLibOnce sync.Once
	txLibPtr  *txbuildercore.Library[any]
	txLibErr  error

	ledgerConstantsOnce sync.Once
	ledgerConstantsPtr  *txbuildercore.Constants
	ledgerConstantsErr  error
)

// GetTxLibrary returns the per-process wallet library, fetched lazily
// from the connected node on first call (latest slot) and cached for
// the lifetime of the proxi command process. Panics if the fetch
// fails — wallet flows can't proceed without it.
func GetTxLibrary() *txbuildercore.Library[any] {
	txLibOnce.Do(func() {
		// the library comes with the node's proof that a branch commits to it, and
		// the witnesses confirm that branch (kb/library_proof.md, kb/api_witnesses.md)
		var c *txbuildercore.LibraryCommitment
		txLibPtr, c, txLibErr = GetClient().GetLibraryWithCommitment(nil)
		if txLibErr == nil {
			txLibErr = VerifyBranchWithWitnesses(c.BranchID)
		}
	})
	AssertNoError(txLibErr)
	return txLibPtr
}

// GetLedgerConstants returns the runtime ledger constants for the
// latest library, fetched lazily on first call and cached for the
// process lifetime. Sits next to GetTxLibrary; together they let a
// proxi command run against a node without InitLedgerFromNode (the
// ledger.L() singleton). See kb/archive/shipped/wallet_eval_api.md.
func GetLedgerConstants() *txbuildercore.Constants {
	ledgerConstantsOnce.Do(func() {
		ledgerConstantsPtr, ledgerConstantsErr = GetClient().GetLedgerConstants(nil)
	})
	AssertNoError(ledgerConstantsErr)
	return ledgerConstantsPtr
}

// GetLedgerTimeNow returns the node's current ledger time via the
// /api/v1/get_ledger_time endpoint. Unlike GetLedgerConstants it is
// NOT cached — the time advances, so every call hits the node. Use it
// for transaction timestamps (the node's authoritative clock) instead
// of converting wall-clock time client-side.
func GetLedgerTimeNow() base.LedgerTime {
	t, err := GetClient().GetLedgerTime()
	AssertNoError(err)
	return t
}

// SubmitAndDisplay submits txBytes via the new /api/v1/submit_tx
// endpoint (validate_only=false).
//
// consumedUTXOBytes is an optional variadic parameter — each entry is
// the raw output wire-bytes for the corresponding tx input
// (positionally aligned with the tx's InputIDs). When non-empty the
// server runs full-context validation before submit. Passing no arg =
// parse + partial-context validation only at submit time.
//
// On submit failure, prints the error + LinesHR (full detail) of the
// failing tx and returns the error. On success, prints LinesHR only
// when --verbose is on.
//
// Pretty-printing is fully wallet-side: it uses ParseLibraryAgnostic
// (no ledger.L() singleton) and the per-process wallet library for
// every bytecode decompilation. The wallet does NOT need (and does
// not call) InitLedgerFromNode to display its own transactions.
func SubmitAndDisplay(txBytes []byte, consumedUTXOBytes ...[]byte) error {
	lib := GetTxLibrary()
	var opts []client.SubmitOption
	if len(consumedUTXOBytes) > 0 {
		opts = append(opts, client.WithConsumedUTXOs(consumedUTXOBytes))
	}

	txID, err := GetClient().SubmitTransactionWithDetail(txBytes, opts...)
	if err != nil {
		Infof("\nFAILED to submit transaction: %v", err)
		Infof("---------- failing tx --------\n%s", transaction.TxDisplay(lib, txBytes, consumedUTXOBytes...))
		return err
	}

	if IsVerbose() {
		Infof("\n-------- tx OK %s (len = %d) -----------\n%s",
			txID.StringHex(), len(txBytes), transaction.TxDisplay(lib, txBytes, consumedUTXOBytes...))
	}
	return nil
}

// ParseTransactionID accepts the hex form and the dashed form of a
// transaction ID, as printed by StringHex and StringDashed.
func ParseTransactionID(s string) (base.TransactionID, error) {
	if txid, err := base.TransactionIDFromHexString(s); err == nil {
		return txid, nil
	}
	txid, err := base.TransactionIDFromStringDashed(s)
	if err != nil {
		return txid, fmt.Errorf("'%s' is neither a hex nor a dashed transaction ID: %v", s, err)
	}
	return txid, nil
}
