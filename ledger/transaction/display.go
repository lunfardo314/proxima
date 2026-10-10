package transaction

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// Wallet-side rendering of a transaction and its outputs with an explicit
// library, for any process that has no ledger singleton: proxi, the
// consolidator, a wasm wallet.

// TxDisplay renders a wallet-side LinesHR-style summary of a tx without
// touching the ledger.L() singleton. Uses ParseLibraryAgnostic
// for the tx skeleton and the supplied wallet library for every
// bytecode decompilation (tx-level constraints, output constraints,
// chain-constraint parse for the produced-output chainID display).
//
// Output bytes are decoded structurally via ledger.OutputFromBytes
// (no validation). Each output's constraints are decompiled
// individually via lib.Decompile so the display works against any
// well-formed branch of the library — no lock-dispatch or
// constraint-record lookup required.
func TxDisplay(lib *txbuildercore.Library[any], txBytes []byte, consumedBytes ...[]byte) string {
	tx, err := ParseLibraryAgnostic(txBytes)
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
	// against. consumedBytes is positionally aligned with InputIDs; an
	// empty entry in a supplied slice means the consumed output could not
	// be obtained.
	ln.Add("Inputs (%d):", tx.NumInputs())
	tx.ForEachInputID(func(idx byte, oid base.OutputID) bool {
		ln.Add("  #%d: %s", idx, oid.String())
		switch {
		case len(consumedBytes) == 0:
		case int(idx) < len(consumedBytes) && len(consumedBytes[idx]) > 0:
			renderOutputBytes(ln, lib, consumedBytes[idx], "       ", oid)
		default:
			ln.Add("       consumed output not available")
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
