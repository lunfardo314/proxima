package glb

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// Wallet-side foundation helpers for the wasm-style refactor. See
// claude/archive/shipped/proxi_txbuildercore.md (Phase 0.3) for context. Tx
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
// ledger.L() singleton). See claude/archive/shipped/wallet_eval_api.md.
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
		Infof("---------- failing tx --------\n%s", txDisplay(lib, txBytes, consumedUTXOBytes...))
		return err
	}

	if IsVerbose() {
		Infof("\n-------- tx OK %s (len = %d) -----------\n%s",
			txID.StringHex(), len(txBytes), txDisplay(lib, txBytes, consumedUTXOBytes...))
	}
	return nil
}

// txDisplay renders a wallet-side LinesHR-style summary of a tx without
// touching the ledger.L() singleton. Uses transaction.ParseLibraryAgnostic
// for the tx skeleton and the supplied wallet library for every
// bytecode decompilation (tx-level constraints, output constraints,
// chain-constraint parse for the produced-output chainID display).
//
// Output bytes are decoded structurally via ledger.OutputFromBytes
// (no validation). Each output's constraints are decompiled
// individually via lib.Decompile so the display works against any
// well-formed branch of the library — no lock-dispatch or
// constraint-record lookup required.
func txDisplay(lib *txbuildercore.Library[any], txBytes []byte, consumedBytes ...[]byte) string {
	tx, err := transaction.ParseLibraryAgnostic(txBytes)
	if err != nil {
		return fmt.Sprintf("ParseLibraryAgnostic returned: %v\n  raw (%d bytes): %s",
			err, len(txBytes), hex.EncodeToString(txBytes))
	}
	ln := lines.New()
	txid := tx.ID()
	ln.Add("Transaction ID: %s, size: %d", txid.String(), len(txBytes))
	ln.Add("Timestamp: %s", tx.Timestamp().String())
	ln.Add("IsBranch: %v", tx.IsBranchTransaction())
	ln.Add("IsSequencer: %v", tx.IsSequencerTransaction())
	if sig, err := tx.Signature(); err == nil {
		ln.Add("Signature: %s", sig.String())
	} else {
		ln.Add("Signature: err='%v'", err)
	}
	if explicitBaseline, ok := tx.ExplicitBaseline(); ok {
		ln.Add("Explicit baseline: %s", explicitBaseline.String())
	}
	ln.Add("Endorsements (%d):", tx.NumEndorsements())
	tx.ForEachEndorsement(func(idx byte, eTxid base.TransactionID) bool {
		ln.Add("  %d: %s", idx, eTxid.String())
		return true
	})

	// Tx-level constraints — decompile via wallet library.
	txConstraintsBin := tx.MustBytesAtPath(ledger.PathToTxConstraints)
	if len(txConstraintsBin) == 0 {
		ln.Add("TxConstraints (0):")
	} else if tcs, err := tuples.TupleFromBytes(txConstraintsBin); err != nil {
		ln.Add("TxConstraints: parse error: %v", err)
	} else {
		ln.Add("TxConstraints (%d):", tcs.NumElements())
		tcs.ForEach(func(i int, bc []byte) bool {
			if src, derr := lib.Decompile(bc); derr == nil {
				ln.Add("  %d: %s  (%d bytes)", i, src, len(bc))
			} else {
				ln.Add("  %d: %d bytes (decompile err: %v)", i, len(bc), derr)
			}
			return true
		})
	}

	// Inputs: print outputID + (when supplied) the full consumed-UTXO
	// rendering so the user sees the same context the server validated
	// against. consumedBytes is positionally aligned with InputIDs.
	ln.Add("Inputs (%d):", tx.NumInputs())
	tx.ForEachInputID(func(idx byte, oid base.OutputID) bool {
		ln.Add("  #%d: %s", idx, oid.String())
		if int(idx) < len(consumedBytes) && len(consumedBytes[idx]) > 0 {
			renderOutputBytes(ln, lib, consumedBytes[idx], "       ", oid)
		}
		return true
	})

	// Produced outputs: walk the raw bytes (singleton-free) and
	// decompile each constraint via the wallet library. For chain
	// outputs, surface the resolved chainID (origin → blake2b(oid)).
	ln.Add("Outputs (%d produced):", tx.NumProducedOutputs())
	totalSum := uint64(0)
	tx.ForEachProducedOutputData(func(idx byte, oData []byte) bool {
		oid := base.MustNewOutputID(txid, idx)
		ln.Add("  #%d %s", idx, oid.String())
		if o := renderOutputBytes(ln, lib, oData, "       ", oid); o != nil {
			totalSum += o.TokenBalance()
		}
		return true
	})
	ln.Add("TOTAL produced token balance: %s", util.Th(totalSum))
	return ln.String()
}

// renderOutputBytes appends a wallet-side rendering of an output to ln
// at the given prefix. Decompiles each constraint via the wallet
// library, handles amounts / index-values specially, and surfaces the
// chainID for chain outputs (resolving origin via blake2b(outputID)).
// Returns the parsed Output (so callers can sum balances), or nil on
// parse error.
func renderOutputBytes(ln *lines.Lines, lib *txbuildercore.Library[any], data []byte, prefix string, oid base.OutputID) *ledger.Output {
	ln.Add("%sbytes (%d): %s", prefix, len(data), hex.EncodeToString(data))
	o, err := ledger.OutputFromBytes(data)
	if err != nil {
		ln.Add("%sparse error: %v", prefix, err)
		return nil
	}
	for j, raw := range o.ConstraintsRawBytes() {
		if len(raw) == 0 {
			continue
		}
		ln.Add("%s[%d] %s", prefix, j, FormatConstraintAtIndex(lib, byte(j), raw))
	}
	if chainBin, cerr := o.ConstraintAt(ledger.ConstraintIndexChain); cerr == nil && len(chainBin) > 0 {
		if cc, ccerr := lib.ParseChainConstraint(chainBin); ccerr == nil {
			cid := cc.ChainID
			origin := ""
			if cid == base.NilChainID {
				cid = base.MakeOriginChainID(oid)
				origin = " (origin)"
			}
			ln.Add("%schainID: %s%s", prefix, cid.StringShort(), origin)
		}
	}
	return o
}

// FormatConstraintAtIndex returns a one-line pretty form of the raw bytes at
// the given constraint index of an output. Index 0 (amounts vector) and index 1
// (index-values tuple) are NOT bytecode — they are structurally parsed; indices
// 2+ are decompiled via the wallet library. Empty `raw` is reported as such
// instead of failing decompile.
//
// Use this everywhere an output is dumped index-by-index, instead of calling
// lib.DecompileBytecode on every position — feeding the amounts/index-values
// bytes through the bytecode decoder produces confusing "wrong function code"
// errors.
func FormatConstraintAtIndex(lib *txbuildercore.Library[any], idx byte, raw []byte) string {
	if len(raw) == 0 {
		return "<empty>"
	}
	switch idx {
	case ledger.ConstraintIndexAmounts:
		return "amounts = " + formatAmounts(raw)
	case ledger.ConstraintIndexIndexValues:
		return "index values: " + formatIndexValues(raw)
	}
	src, err := lib.Decompile(raw)
	if err != nil {
		return fmt.Sprintf("<decompile error: %v>", err)
	}
	return src
}

// formatAmounts pretty-prints the amounts vector at constraint slot 0.
// Singleton-free: ledger.AmountsFromBytes is a structural byte parse.
func formatAmounts(raw []byte) string {
	a, err := ledger.AmountsFromBytes(raw)
	if err != nil {
		return fmt.Sprintf("(parse error: %v; %d bytes hex: %s)", err, len(raw), hex.EncodeToString(raw))
	}
	parts := make([]string, 0, 4)
	parts = append(parts, util.Th(a.TokenBalance()))
	if infl := a.InflationAmount(); infl != 0 {
		parts = append(parts, "inflation: "+util.Th(infl))
	}
	// the encoded cells, not one line per epoch: a delegation has a single cell
	// covering its whole span, a sequencer aggregate one per step of its
	// staircase. The last one runs to the bound.
	if bound := a.FrozenCoverageBound(); bound > 0 {
		cells := make([]string, 0, 4)
		for i := int(ledger.AmountIndexFrozenCoverage); i < a.NumElements(); i++ {
			cells = append(cells, util.Th(a.Amount(byte(i))))
		}
		parts = append(parts, fmt.Sprintf("frozen coverage over %d epoch(s): %s",
			bound, strings.Join(cells, ", ")))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// formatIndexValues pretty-prints the index-value tuple at constraint
// slot 1. Each element is hex-encoded so the controllers / hashes are
// human-readable. No `0x` prefix on entries — these are raw indexed bytes,
// not EasyFL inline-data literals.
func formatIndexValues(raw []byte) string {
	t, err := tuples.TupleFromBytes(raw)
	if err != nil {
		return fmt.Sprintf("(parse error: %v; %d bytes hex: %s)", err, len(raw), hex.EncodeToString(raw))
	}
	parts := make([]string, 0, t.NumElements())
	t.ForEach(func(_ int, v []byte) bool {
		parts = append(parts, hex.EncodeToString(v))
		return true
	})
	return "[" + strings.Join(parts, ", ") + "]"
}
