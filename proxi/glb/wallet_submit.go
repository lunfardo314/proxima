package glb

import (
	"sync"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// Wallet-side foundation helpers for the wasm-style refactor. See
// claude/proxi_txbuildercore.md (Phase 0.3) for context. Tx
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
		txLibPtr, txLibErr = GetClient().GetLibrary(nil)
	})
	AssertNoError(txLibErr)
	return txLibPtr
}

// GetLedgerConstants returns the runtime ledger constants for the
// latest library, fetched lazily on first call and cached for the
// process lifetime. Sits next to GetTxLibrary; together they let a
// proxi command run against a node without InitLedgerFromNode (the
// ledger.L() singleton). See claude/wallet_eval_api.md.
func GetLedgerConstants() *txbuildercore.Constants {
	ledgerConstantsOnce.Do(func() {
		ledgerConstantsPtr, ledgerConstantsErr = GetClient().GetLedgerConstants(nil)
	})
	AssertNoError(ledgerConstantsErr)
	return ledgerConstantsPtr
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
// Pretty-printing uses transaction.LinesFromTransactionBytesWithLib
// with a decompiler built from the wallet library — no ledger.L()
// singleton dependency at the surface, though Output rendering still
// uses the singleton internally (see transaction.Decompiler doc).
func SubmitAndDisplay(txBytes []byte, consumedUTXOBytes ...[]byte) error {
	lib := GetTxLibrary()
	var opts []client.SubmitOption
	if len(consumedUTXOBytes) > 0 {
		opts = append(opts, client.WithConsumedUTXOs(consumedUTXOBytes))
	}

	txID, err := GetClient().SubmitTransactionWithDetail(txBytes, opts...)
	if err != nil {
		Infof("\nFAILED to submit transaction: %v", err)
		Infof("---------- failing tx --------\n%s", txDisplay(lib, txBytes))
		return err
	}

	if IsVerbose() {
		Infof("\n-------- tx OK %s (len = %d) -----------\n%s",
			txID.StringHex(), len(txBytes), txDisplay(lib, txBytes))
	}
	return nil
}

// txDisplay renders the LinesHR form of a tx for log output. Uses the
// wallet library for tx-level constraint decompilation; output
// rendering inside Output._lines still reaches the singleton (see
// transaction.Decompiler doc).
func txDisplay(lib *txbuildercore.Library[any], txBytes []byte) string {
	return transaction.LinesFromTransactionBytesWithLib(lib, txBytes, nil).String()
}
