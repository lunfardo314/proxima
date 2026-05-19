// Probe entry point for measuring the wasm wallet's transaction-builder
// binary size under TinyGo. Exercises the txcore compose + sign path
// without depending on a parsed library (the lock bytecode is provided
// as a raw byte placeholder), so this is the FLOOR measurement —
// everything the wallet pays for to build and sign a tx, minus the
// library-aware constraint helpers.
//
// See claude/wasm_txbuilder.md Phase 5.
//
// Build: tinygo build -target=wasm -o /tmp/txcore.wasm ./ledger/txcore/wasm/
package main

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txcore"
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

	txb := txcore.New(0)

	// One pretend-consumed output (sigLock-shaped, but with a
	// placeholder lock — we measure without invoking the library
	// path here to isolate the txbuilder + sign cost).
	consumed := txcore.NewOutputBuilder()
	consumed.PutConstraint(txcore.EncodeTokenBalance(1_000_000_000), txcore.ConstraintIndexAmounts)
	consumed.PutConstraint(txcore.EncodeIndexValuesTuple([][]byte{make([]byte, 32)}), txcore.ConstraintIndexIndexValues)
	consumed.PutConstraint([]byte{0x80}, txcore.ConstraintIndexLock)

	var oidTxid base.TransactionID
	oid := base.MustNewOutputID(oidTxid, 0)
	txb.ConsumeOutput(consumed.Bytes(), oid)

	// One pretend-produced output, same shape.
	produced := txcore.NewOutputBuilder()
	produced.PutConstraint(txcore.EncodeTokenBalance(900_000_000), txcore.ConstraintIndexAmounts)
	produced.PutConstraint(txcore.EncodeIndexValuesTuple([][]byte{make([]byte, 32)}), txcore.ConstraintIndexIndexValues)
	produced.PutConstraint([]byte{0x80}, txcore.ConstraintIndexLock)
	txb.ProduceOutput(produced.Bytes())

	txb.PutSignatureUnlock(0)
	txb.SetTimestamp(base.T(0, 1))
	txb.ComputeInputCommitment()
	txb.SignED25519(priv)

	sink = len(txb.Bytes())
}
