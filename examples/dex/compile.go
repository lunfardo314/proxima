// Package dex implements the Phase-1 PoC of the DEX order locks described in
// claude/dex_orders.md. The whole covenant (sellOrder, buyOrder,
// randomizeConsumption + internal helpers) compiles into a single local-script
// binary that the consuming transaction commits via `redeemScript(<bin>)` and
// invokes from each order UTXO's lock element via
// `callRedeemer(<dexHash>, <fnIdx>, args...)`.
//
// Privacy surface relies on easyfl's underscore-private convention — every
// helper here is `_`-prefixed, so `callRedeemer` can only reach the three
// public entries.
package dex

import (
	_ "embed"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"golang.org/x/crypto/blake2b"
)

//go:embed dex.easyfl
var dexSource string

// Bins holds the compiled dex local-script binary and the per-entry function
// indices needed to build callRedeemer dispatch bytecode.
type Bins struct {
	Bin  easyfl.LocalScriptBin
	Hash [32]byte

	SellOrderIdx            int
	BuyOrderIdx             int
	RandomizeConsumptionIdx int
}

var (
	binsOnce sync.Once
	binsVal  *Bins
	binsErr  error
)

// GetBins returns the singleton compiled dex covenant. Panics on compile
// error (PoC authors hit this at init time, not at tx-validation time).
func GetBins() *Bins {
	binsOnce.Do(func() { binsVal, binsErr = buildBins() })
	if binsErr != nil {
		panic(binsErr)
	}
	return binsVal
}

func buildBins() (*Bins, error) {
	lib := ledger.L(base.MaxSlot)

	bin, idx, err := lib.CompileLocalScriptWithIndex(dexSource)
	if err != nil {
		return nil, fmt.Errorf("dex: compile bundle: %w\nsource:\n%s", err, dexSource)
	}

	sellIdx, ok := idx["sellOrder"]
	if !ok {
		return nil, fmt.Errorf("dex: bundle has no public sellOrder entry")
	}
	buyIdx, ok := idx["buyOrder"]
	if !ok {
		return nil, fmt.Errorf("dex: bundle has no public buyOrder entry")
	}
	randIdx, ok := idx["randomizeConsumption"]
	if !ok {
		return nil, fmt.Errorf("dex: bundle has no public randomizeConsumption entry")
	}
	if sellIdx > 0xff || buyIdx > 0xff || randIdx > 0xff {
		return nil, fmt.Errorf("dex: fnIdx > 255 (sell=%d buy=%d rand=%d)", sellIdx, buyIdx, randIdx)
	}

	return &Bins{
		Bin:                     bin,
		Hash:                    blake2b.Sum256(bin),
		SellOrderIdx:            sellIdx,
		BuyOrderIdx:             buyIdx,
		RandomizeConsumptionIdx: randIdx,
	}, nil
}

// SellOrderLockBytecode compiles the order UTXO's lock element for a sell
// order: callRedeemer(<dexHash>, <sellOrderFnIdx>, price, timeoutSlots).
func SellOrderLockBytecode(price uint64, timeoutSlots uint32) ([]byte, error) {
	b := GetBins()
	src := fmt.Sprintf("callRedeemer(0x%s, 0x%02x, z64/%d, z32/%d)",
		hex.EncodeToString(b.Hash[:]), b.SellOrderIdx, price, timeoutSlots)
	_, _, bc, err := ledger.L(base.MaxSlot).CompileExpression(src)
	if err != nil {
		return nil, fmt.Errorf("dex: SellOrderLockBytecode: %w", err)
	}
	return bc, nil
}

// BuyOrderLockBytecode compiles the lock element for a buy order:
// callRedeemer(<dexHash>, <buyOrderFnIdx>, amount, price, timeoutSlots).
func BuyOrderLockBytecode(amount, price uint64, timeoutSlots uint32) ([]byte, error) {
	b := GetBins()
	src := fmt.Sprintf("callRedeemer(0x%s, 0x%02x, z64/%d, z64/%d, z32/%d)",
		hex.EncodeToString(b.Hash[:]), b.BuyOrderIdx, amount, price, timeoutSlots)
	_, _, bc, err := ledger.L(base.MaxSlot).CompileExpression(src)
	if err != nil {
		return nil, fmt.Errorf("dex: BuyOrderLockBytecode: %w", err)
	}
	return bc, nil
}

// RandomizeConsumptionBytecode compiles an optional anti-contention
// constraint that can sit at any free position on an order UTXO.
func RandomizeConsumptionBytecode(n uint8) ([]byte, error) {
	b := GetBins()
	src := fmt.Sprintf("callRedeemer(0x%s, 0x%02x, z16/%d)",
		hex.EncodeToString(b.Hash[:]), b.RandomizeConsumptionIdx, n)
	_, _, bc, err := ledger.L(base.MaxSlot).CompileExpression(src)
	if err != nil {
		return nil, fmt.Errorf("dex: RandomizeConsumptionBytecode: %w", err)
	}
	return bc, nil
}

// RedeemScriptConstraint returns the TxConstraints-level constraint that
// commits the dex binary to a consuming transaction. Push this onto the
// tx via txbuilder.PushTxConstraint exactly once per swap tx — every
// callRedeemer invocation in the tx then resolves to the same binary.
func RedeemScriptConstraint() ([]byte, error) {
	b := GetBins()
	src := fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(b.Bin))
	_, _, bc, err := ledger.L(base.MaxSlot).CompileExpression(src)
	if err != nil {
		return nil, fmt.Errorf("dex: RedeemScriptConstraint: %w", err)
	}
	return bc, nil
}

// SourceForDebug returns the dex source as a string. Useful when the
// compile error is opaque and you want to grep the file from a test.
func SourceForDebug() string { return dexSource }
