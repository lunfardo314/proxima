package transaction

import (
	"bytes"
	"encoding/hex"
	"slices"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"golang.org/x/crypto/blake2b"
)

// Decompiler is the minimal library surface needed by the tx-level
// pretty-printer: bytecode decompile + one-level parse. Both
// *ledger.Library (server / singleton path) and *txbuildercore.Library[T]
// (wallet path) satisfy it; the non-generic Decompile method on each
// hides the engine.Library type-parameterised variadic so a single
// interface can bind both library instantiations.
//
// Output._lines still uses the typed-constraint serdes registered on
// *ledger.Library (via ConstraintFromBytesWithLib) and therefore does
// NOT participate in this abstraction — the wallet path can still
// pretty-print outputs as long as proxi has initialised the ledger
// library at startup.
type Decompiler interface {
	Decompile(code []byte) (string, error)
	ParseBytecodeOneLevel(code []byte, expectedNumArgs ...int) (string, []byte, [][]byte, error)
}

func (tx *Transaction) Lines(inputLoaderByIndex func(i byte) ([]byte, error), prefix ...string) *lines.Lines {
	return tx.LinesWithLib(ledger.L(base.MaxSlot), inputLoaderByIndex, prefix...)
}

// LinesWithLib is the wallet-friendly form of Lines: the caller
// supplies the decompiler. Existing callers can use Lines (which
// resolves to the singleton).
func (tx *Transaction) LinesWithLib(lib Decompiler, inputLoaderByIndex func(i byte) ([]byte, error), prefix ...string) *lines.Lines {
	if inputLoaderByIndex != nil {
		if err := tx.SetFullContext(inputLoaderByIndex); err != nil {
			ret := lines.New(prefix...)
			ret.Add("can't create context of transaction %s: '%v'", tx.IDShortString(), err)
			return ret
		}
	}
	return tx.LinesHRWithLib(lib, prefix...)
}

func (tx *Transaction) ProducedTagAlongOutputs(targetID ...base.ChainID) []ledger.TagAlongOutput {
	ret := make([]ledger.TagAlongOutput, 0)
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid base.OutputID) bool {
		out := ledger.OutputWithID{ID: oid, Output: o}
		ta := out.AsTagAlong()
		if ta.TagAlongLock == nil {
			return true
		}
		if len(targetID) > 0 && ta.TagAlongLock.TargetSequencerID != targetID[0] {
			return true
		}
		ret = append(ret, ta)
		return true
	})
	return ret
}

func (tx *Transaction) LinesShort(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("id: %s", tx.IDString())
	sig, err := tx.Signature()
	util.AssertNoError(err)
	ret.Add("Holder ID: %s", sig.HolderIDHex())
	ret.Add("Total: %s", util.Th(tx.TotalAmount()))
	ret.Add("Inflation: %s", util.Th(tx.InflationAmount()))
	if tx.IsSequencerTransaction() {
		ret.Add("Sequencer output index: %d, Stem output index: %d", tx.sequencerTransactionData.SequencerOutputIndex, tx.sequencerTransactionData.StemOutputIndex)
	}
	ret.Add("Endorsements (%d):", tx.NumEndorsements())
	tx.ForEachEndorsement(func(idx byte, txid base.TransactionID) bool {
		ret.Add("    %3d: %s", idx, txid.String())
		return true
	})
	ret.Add("Inputs (%d):", tx.NumInputs())
	tx.ForEachInputID(func(i byte, oid base.OutputID) bool {
		ret.Add("    %3d: %s", i, oid.String())
		ret.Add("       Unlock data: %s", UnlockDataToString(tx.MustUnlockDataAt(i)))
		return true
	})
	ret.Add("Outputs (%d):", tx.NumProducedOutputs())
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
		ret.Add("%s", oid.StringShort())
		ret.Append(o.Lines(pref + "    "))
		return true
	})
	return ret
}

func (tx *Transaction) LinesSource(prefix ...string) *lines.Lines {
	return tx.LinesSourceWithLib(ledger.L(base.MaxSlot), prefix...)
}

// LinesSourceWithLib is the wallet-friendly form of LinesSource.
// Output rendering still uses *ledger.Library internally (typed
// constraint serdes); the lib parameter only affects how tx-level
// constraints (TxConstraints slot) are decompiled.
func (tx *Transaction) LinesSourceWithLib(lib Decompiler, prefix ...string) *lines.Lines {
	return tx._lines(lib, func(o *ledger.Output, prefix ...string) *lines.Lines {
		return o.LinesSource(prefix...)
	}, prefix...)
}

func (tx *Transaction) LinesHR(prefix ...string) *lines.Lines {
	return tx.LinesHRWithLib(ledger.L(base.MaxSlot), prefix...)
}

// LinesHRWithLib is the wallet-friendly form of LinesHR. See
// LinesSourceWithLib for the constraint-rendering caveat.
func (tx *Transaction) LinesHRWithLib(lib Decompiler, prefix ...string) *lines.Lines {
	return tx._lines(lib, func(o *ledger.Output, prefix ...string) *lines.Lines {
		return o.LinesHR(prefix...)
	}, prefix...)
}

func (tx *Transaction) _lines(lib Decompiler, utxoToLines func(o *ledger.Output, prefix ...string) *lines.Lines, prefix ...string) *lines.Lines {
	txid := tx.ID()
	ret := lines.New(prefix...)
	ret.Add("Transaction ID: %s, size: %d", txid.String(), len(tx.Bytes()))
	ret.Add("TxVersion: %d", tx.Version())
	ts := tx.Timestamp()
	ret.Add("Timestamp: %s", ts.String())

	if seqData := tx.SequencerTransactionData(); seqData != nil {
		ret.Add("SEQUENCER TRANSACTION DATA:")
		ret.Append(seqData.Lines("    "))
	} else {
		ret.Add("NOT A SEQUENCER TRANSACTION")
	}

	ret.Add("Total consumed token balance: %s", util.Th(tx.totalConsumedTokenBalance))
	ret.Add("Total produced amounts: [%s]", util.ThSlice(tx.producedAmountTotals[:]...))

	inpCom := tx.InputCommitment()
	ret.Add("Input commitment: %s", easyfl_util.Fmt(inpCom))
	if tx.IsPartialContext() {
		ret.Add("Consumed output hash: N/A")
	} else {
		h := tx.ConsumedOutputHash()
		eqCom := ""
		if !bytes.Equal(inpCom, h[:]) {
			eqCom = "   !!! NOT EQUAL WITH INPUT COMMITMENT !!!!"
		}
		ret.Add("Consumed output hash: %s%s", easyfl_util.Fmt(h[:]), eqCom)
	}
	sign, err := tx.Signature()
	if err == nil {
		ret.Add("Signature: %s", sign.String())
	} else {
		ret.Add("Signature: err='%v'", err)
	}

	if explicitBaseline, ok := tx.ExplicitBaseline(); ok {
		ret.Add("Explicit baseline: %s", explicitBaseline.String())
	}

	ret.Add("Endorsements (%d):", tx.NumEndorsements())
	tx.ForEachEndorsement(func(idx byte, txid base.TransactionID) bool {
		ret.Add("  %d: %s", idx, txid.String())
		return true
	})

	// Tx-level constraints (path 0x000a). Decompile each and surface the
	// content hash of every commitment. For `redeemScript(0x<bin>)` the
	// hash printed is blake2b(bin) — the value that gets registered in
	// the tx's redeemed-set and that callRedeemer literals must match.
	// For any other tx-level constraint the hash is blake2b of the
	// constraint bytecode (a stable identity for it).
	//
	// Printed before Inputs so the reader knows which local scripts the
	// tx commits to before reading any callRedeemer call sites.
	txConstraintsBin := tx.MustBytesAtPath(ledger.PathToTxConstraints)
	if len(txConstraintsBin) == 0 {
		ret.Add("TxConstraints (0):")
	} else {
		tcs, err := tuples.TupleFromBytes(txConstraintsBin)
		if err != nil {
			ret.Add("TxConstraints: parse error: %v", err)
		} else {
			ret.Add("TxConstraints (%d):", tcs.NumElements())
			tcs.ForEach(func(i int, bc []byte) bool {
				hashLabel, hashHex := txConstraintHash(lib, bc)
				src, err := lib.Decompile(bc)
				if err != nil {
					ret.Add("  %d: %d bytes (decompile err: %v)", i, len(bc), err)
				} else {
					ret.Add("  %d: %s  (%d bytes)", i, src, len(bc))
				}
				if hashLabel != "" {
					ret.Add("     %s: %s", hashLabel, hashHex)
				}
				return true
			})
		}
	}

	ret.Add("Inputs (%d): ", tx.NumInputs())
	if tx.IsPartialContext() {
		ret.Add("Consumed UTXOs N/A", tx.NumInputs())
	} else {
		tx.ForEachConsumedOutput(func(idx byte, o ledger.OutputWithID) bool {
			unlockBin := tx.MustUnlockDataAt(idx)
			ret.Add("  #%d: %s", idx, o.ID.String()).
				Add("       bytes (%d): %s", len(o.Bytes()), hex.EncodeToString(o.Bytes())).
				Append(utxoToLines(o.Output, "     ")).
				Add("     Unlock data: %s", UnlockDataToString(unlockBin))
			return true
		})
	}

	ret.Add("Outputs (%d produced): ", tx.NumProducedOutputs())
	totalSum := uint64(0)
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
		totalSum += o.TokenBalance()
		chainIdStr := ""
		if cc := o.ChainConstraint(); cc != nil {
			var cid base.ChainID
			if cc.IsOrigin() {
				oid1 := base.MustNewOutputID(txid, idx)
				cid = base.MakeOriginChainID(oid1)
			} else {
				cid = cc.ChainID
			}
			chainIdStr = "                      chainID: " + cid.StringShort()
		}
		ret.Add("  #%d %s", idx, oid.String()).
			Add("       bytes (%d): %s", len(o.Bytes()), hex.EncodeToString(o.Bytes()))
		if msd, err := ledger.ParseSequencerData(o); err == nil {
			ret.Add("       seq: %s", msd.Name())
		}
		ret.Append(utxoToLines(o, "     ")).
			Add(chainIdStr)
		return true
	})
	ret.Add("TOTAL: %s", util.Th(totalSum))
	return ret
}

func (tx *Transaction) String() string {
	return tx.LinesHR().String()
}

func LinesFromTransactionBytes(txBytes []byte, inputLoader func(i byte) ([]byte, error), prefix ...string) *lines.Lines {
	return LinesFromTransactionBytesWithLib(ledger.L(base.MaxSlot), txBytes, inputLoader, prefix...)
}

// LinesFromTransactionBytesWithLib is the wallet-friendly form. The
// caller supplies the decompiler; output-rendering still uses the
// singleton internally (see LinesSourceWithLib).
func LinesFromTransactionBytesWithLib(lib Decompiler, txBytes []byte, inputLoader func(i byte) ([]byte, error), prefix ...string) *lines.Lines {
	tx, err := Parse(txBytes)
	if err != nil {
		return lines.New(prefix...).Add("Parse returned: %v", err)
	}
	return tx.LinesWithLib(lib, inputLoader, prefix...)
}

// txConstraintHash returns a (label, hex) pair identifying the content of a
// tx-level constraint, but only when the value is operationally meaningful:
// for `redeemScript(<bin>)` it returns blake2b(bin) — the literal a
// callRedeemer site must match. For any other constraint it returns
// ("", "") so the caller skips the line entirely (the constraint's source
// is already printed and a blake2b of arbitrary bytecode adds no signal).
func txConstraintHash(lib Decompiler, bc []byte) (label, hexStr string) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(bc)
	if err == nil && sym == ledger.SymRedeemScript && len(args) == 1 {
		bin := easyfl.StripDataPrefix(args[0])
		h := blake2b.Sum256(bin)
		return "redeemed hash", hex.EncodeToString(h[:])
	}
	return "", ""
}

func UnlockDataToString(data []byte) string {
	arr, err := tuples.TupleFromBytes(data)
	if err != nil {
		return err.Error()
	}
	return arr.String()
}

func ParseBytesToString(txBytes []byte, fetchOutput func(oid base.OutputID) ([]byte, bool)) string {
	tx, err := Parse(txBytes)
	if err != nil {
		return err.Error()
	}
	if err = tx.SetFullContext(tx.InputLoaderByIndex(fetchOutput)); err != nil {
		return err.Error()
	}
	return tx.String()
}

func PickOutputFromListFunc(lst []*ledger.OutputWithID) func(oid base.OutputID) ([]byte, bool) {
	return func(oid base.OutputID) ([]byte, bool) {
		idx := slices.IndexFunc(lst, func(o *ledger.OutputWithID) bool {
			return o.ID == oid
		})
		if idx < 0 {
			return nil, false
		}
		return lst[idx].Output.Bytes(), true
	}
}
