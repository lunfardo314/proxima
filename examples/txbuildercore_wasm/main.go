// txbuildercore_wasm — end-to-end demo of composing + signing a Proxima
// transaction entirely with ledger/txbuildercore. Builds with standard Go
// and (the point) with TinyGo to WebAssembly:
//
//	go run ./examples/txbuildercore_wasm/
//	tinygo build -target=wasm -o /tmp/txbuildercore_demo.wasm ./examples/txbuildercore_wasm/
//
// See README.md in this directory for the wasm-side glue notes.
//
// What it shows:
//
//  1. Load the library.json (snapshot of the host's compiled ledger
//     library). A production wallet normally downloads this from the
//     node it talks to (HTTP API) and caches it locally; the demo
//     embeds it via //go:embed only so `go run` / `tinygo build` are
//     self-contained. Parsing goes through encoding/json into the
//     descriptor type — no host call-out at compose time.
//  2. Build a txbuildercore.Library[any] from that descriptor.
//  3. Compose a one-input, one-output sigLock transfer using the
//     txbuildercore wallet helpers (NewSigLockOutput).
//  4. Sign with ed25519.
//  5. Print the raw tx hex.
//
// This is exactly what a browser wallet's wasm module would do at
// "build a transfer transaction" time. Replace the println at the
// end with the host glue's JS-callable export.
package main

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/lunfardo314/easyfl/engine"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// library.json is the canonical compiled ledger library for this
// wasm bundle. The wallet ships a new wasm binary whenever the host
// upgrades its library; the wasm binary and host library hashes
// must match for the wallet's bytecode emission to be accepted.
//
// Regenerate with: go run ./examples/txbuildercore_wasm/genlib/
//
//go:embed library.json
var libraryJSON []byte

func main() {
	// Step 1 + 2 — parse + construct the library.
	var desc engine.LibraryFromJSON
	if err := json.Unmarshal(libraryJSON, &desc); err != nil {
		panic(fmt.Errorf("parse library.json: %w", err))
	}
	lib, err := txbuildercore.NewLibrary(&desc)
	if err != nil {
		panic(fmt.Errorf("txbuildercore.NewLibrary: %w", err))
	}

	// Two deterministic ed25519 keys: sender (signs) and recipient.
	senderSeed := make([]byte, ed25519.SeedSize)
	for i := range senderSeed {
		senderSeed[i] = byte(i + 1)
	}
	senderPriv := ed25519.NewKeyFromSeed(senderSeed)
	senderPub := senderPriv.Public().(ed25519.PublicKey)
	senderID := base.HolderIDFromPublicKey(base.SignatureTypeED25519, senderPub)

	recipientSeed := make([]byte, ed25519.SeedSize)
	for i := range recipientSeed {
		recipientSeed[i] = byte(i + 100)
	}
	recipientPriv := ed25519.NewKeyFromSeed(recipientSeed)
	recipientPub := recipientPriv.Public().(ed25519.PublicKey)
	recipientID := base.HolderIDFromPublicKey(base.SignatureTypeED25519, recipientPub)

	// Step 3 — compose the inputs / outputs the wallet got from the
	// host (here we synthesise a single consumed UTXO so the demo is
	// self-contained).
	const (
		consumedAmount uint64 = 100_000_000
		sendAmount     uint64 = 60_000_000
		// The remainder goes back to sender as a "change" output.
	)
	changeAmount := consumedAmount - sendAmount

	consumed, err := txbuildercore.NewSigLockOutput(lib, consumedAmount, senderID)
	if err != nil {
		panic(fmt.Errorf("compose consumed: %w", err))
	}
	produced, err := txbuildercore.NewSigLockOutput(lib, sendAmount, recipientID)
	if err != nil {
		panic(fmt.Errorf("compose produced: %w", err))
	}
	change, err := txbuildercore.NewSigLockOutput(lib, changeAmount, senderID)
	if err != nil {
		panic(fmt.Errorf("compose change: %w", err))
	}

	// Step 4 — assemble + sign.
	upgradeIndex := uint16(0) // wallet bakes this at build time
	txb := txbuildercore.New(upgradeIndex)

	var consumedTxID base.TransactionID // zero-id placeholder
	consumedOID := base.MustNewOutputID(consumedTxID, 0)
	txb.ConsumeOutput(consumed.Bytes(), consumedOID)

	txb.ProduceOutput(produced.Bytes())
	txb.ProduceOutput(change.Bytes())

	txb.PutSignatureUnlock(0)
	txb.SetTimestamp(base.T(1, 12))
	txb.ComputeInputCommitment()
	txb.SignED25519(senderPriv)

	rawTx := txb.Bytes()

	// Step 5 — emit the bytes. A real wasm export would return them
	// across the host ABI; here we just print.
	fmt.Printf("library hash:    %s\n", desc.Hash)
	fmt.Printf("sender holder:   %s\n", hex.EncodeToString(senderID[:]))
	fmt.Printf("recipient holder: %s\n", hex.EncodeToString(recipientID[:]))
	fmt.Printf("tx bytes (%d):    %s\n", len(rawTx), hex.EncodeToString(rawTx))
}
