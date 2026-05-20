// Probe entry point for measuring the wasm wallet's transaction-builder
// binary size under TinyGo. Exercises the txbuildercore compose + sign path
// without depending on a parsed library (the lock bytecode is provided
// as a raw byte placeholder), so this is the FLOOR measurement —
// everything the wallet pays for to build and sign a tx, minus the
// library-aware constraint helpers.
//
// See claude/wasm_txbuilder.md Phase 5.
//
// Build: tinygo build -target=wasm -o /tmp/txbuildercore.wasm ./ledger/txbuildercore/wasm/
package main

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// sink keeps the result reachable so the linker can't DCE the
// compose path away.
var sink int

func main() {
	// A deterministic ed25519 key for reproducible probing.
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)

	txb := txbuildercore.New(0)

	// One pretend-consumed output (sigLock-shaped, but with a
	// placeholder lock — we measure without invoking the library
	// path here to isolate the txbuilder + sign cost).
	consumed := txbuildercore.NewOutputBuilder()
	consumed.PutConstraint(txbuildercore.EncodeTokenBalance(1_000_000_000), txbuildercore.ConstraintIndexAmounts)
	consumed.PutConstraint(txbuildercore.EncodeIndexValuesTuple([][]byte{make([]byte, 32)}), txbuildercore.ConstraintIndexIndexValues)
	consumed.PutConstraint([]byte{0x80}, txbuildercore.ConstraintIndexLock)

	var oidTxid base.TransactionID
	oid := base.MustNewOutputID(oidTxid, 0)
	txb.ConsumeOutput(consumed.Bytes(), oid)

	// One pretend-produced output, same shape.
	produced := txbuildercore.NewOutputBuilder()
	produced.PutConstraint(txbuildercore.EncodeTokenBalance(900_000_000), txbuildercore.ConstraintIndexAmounts)
	produced.PutConstraint(txbuildercore.EncodeIndexValuesTuple([][]byte{make([]byte, 32)}), txbuildercore.ConstraintIndexIndexValues)
	produced.PutConstraint([]byte{0x80}, txbuildercore.ConstraintIndexLock)
	txb.ProduceOutput(produced.Bytes())

	txb.PutSignatureUnlock(0)
	txb.SetTimestamp(base.T(0, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)

	sink = len(txb.Bytes())
}
