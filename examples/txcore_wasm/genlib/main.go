// Helper that regenerates examples/txcore_wasm/library.json from the
// current ledger library. Run from the repo root:
//
//	go run ./examples/txcore_wasm/genlib/
//
// The output file is committed alongside main.go so the wasm demo
// builds without a host call-out. Re-run whenever the ledger library
// definitions change (proxi util compile_ledger_def covers the same
// ground for the node's on-disk definitions; this tiny helper just
// writes the JSON the wasm demo embeds).
package main

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

func main() {
	// Build the ledger library using the same testing parameters the
	// rest of the project uses for golden snapshots.
	priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	params := ledger.DefaultParameters(priv, 1, "txcore wasm demo")
	lib := ledger.LibraryFromParameters(params)

	// compiled=true preserves funCodes + bytecodes + hash; indent=false
	// for compact wire form (the demo decodes this verbatim).
	jsonBytes := easyfl.ToJSON(lib.Library, true, false)

	out, err := filepath.Abs("examples/txcore_wasm/library.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs path: %v\n", err)
		os.Exit(1)
	}
	if err = os.WriteFile(out, jsonBytes, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", out, err)
		os.Exit(1)
	}
	h := lib.LibraryHash()
	fmt.Printf("wrote %d bytes to %s\n", len(jsonBytes), out)
	fmt.Printf("library hash: %x\n", h[:])
	_ = base.MaxSlot
}
