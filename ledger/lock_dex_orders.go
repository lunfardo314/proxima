package ledger

import (
	"bytes"
	"encoding/binary"
	_ "embed"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
)

// =============================================================================
// DEX order locks — graduated Phase-2 implementation. Spec: claude/dex_orders.md.
//
// The lock body lives in def/lock_dex_orders.easyfl (shared helpers + three
// public symbols: sellOrder, buyOrder, randomizeConsumption). This file
// provides:
//
//   - typed Go wrappers SellOrderLock / BuyOrderLock (Lock interface);
//   - bytecode helpers for randomizeConsumption (it's an additive constraint,
//     not a Lock kind);
//   - register* hooks invoked from registerConstraints0.
// =============================================================================

//go:embed def/lock_dex_orders.easyfl
var lockDexOrdersSource string

// Public symbol names.
const (
	SellOrderLockName            = "sellOrder"
	BuyOrderLockName             = "buyOrder"
	RandomizeConsumptionName     = "randomizeConsumption"
	dexOrderBookPrefix           = "ORDR" // 4-byte ASCII prefix; mirrors _ordrPrefix in easyfl
	dexOrderEntrySideBuyByte     = byte(0x00)
	dexOrderEntrySideSellByte    = byte(0x01)
	DexOrderMinTimeoutSlots      = uint32(12) // mirror _minTimeoutSlots
	DexRandomizeMinN             = uint8(2)
	DexRandomizeMaxN             = uint8(32)
)

// dexOrderEntry returns the slot-1 position-1 entry "ORDR || tag || sideByte".
func dexOrderEntry(tag base.ChainID, side byte) []byte {
	out := make([]byte, 0, 4+base.ChainIDLength+1)
	out = append(out, dexOrderBookPrefix...)
	out = append(out, tag[:]...)
	out = append(out, side)
	return out
}

// -----------------------------------------------------------------------------
// SellOrderLock
// -----------------------------------------------------------------------------

// SellOrderLock locks a UTXO that offers `Amount` native tokens of `Tag` for
// `Price` base tokens per token. The order UTXO MUST also carry
// tokenAmount(Tag, Amount) at constraint index 3.
//
// Index-value tuple at output element index 1: [SellerHolderID, "ORDR"||Tag||0x01].
// The lock bytecode at element 2 is sellOrder(Price, TimeoutSlots).
type SellOrderLock struct {
	SellerHolderID base.HolderID
	Tag            base.ChainID
	Price          uint64
	TimeoutSlots   uint32
}

const sellOrderTemplate = SellOrderLockName + "(z64/%d, z32/%d)"

func (l *SellOrderLock) Name() string { return SellOrderLockName }

func (l *SellOrderLock) Source() string {
	return fmt.Sprintf(sellOrderTemplate, l.Price, l.TimeoutSlots)
}

func (l *SellOrderLock) LockBytecode() []byte { return mustBinFromSource(l.Source()) }

func (l *SellOrderLock) IndexValues() [][]byte {
	return [][]byte{l.SellerHolderID[:], dexOrderEntry(l.Tag, dexOrderEntrySideSellByte)}
}

func (l *SellOrderLock) String() string {
	return fmt.Sprintf("sellOrder(seller=%s, tag=%s, price=%d, timeoutSlots=%d)",
		hex.EncodeToString(l.SellerHolderID[:]), l.Tag.String(), l.Price, l.TimeoutSlots)
}

// SellOrderLockFromOutputElements rebuilds a SellOrderLock from a UTXO's
// position-1 (index-values) and position-2 (lock bytecode) elements.
func SellOrderLockFromOutputElements(indexValuesBytes, lockBytecode []byte, lib *Library) (*SellOrderLock, error) {
	tag, holder, err := parseOrderIndexValues(indexValuesBytes, dexOrderEntrySideSellByte)
	if err != nil {
		return nil, fmt.Errorf("SellOrderLockFromOutputElements: %w", err)
	}
	sym, _, args, err := lib.ParseBytecodeOneLevel(lockBytecode, 2)
	if err != nil {
		return nil, fmt.Errorf("SellOrderLockFromOutputElements: %w", err)
	}
	if sym != SellOrderLockName {
		return nil, fmt.Errorf("SellOrderLockFromOutputElements: expected %s, got %s", SellOrderLockName, sym)
	}
	price, err := uint64FromArgBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, fmt.Errorf("SellOrderLockFromOutputElements: price: %w", err)
	}
	timeoutSlots, err := uint32FromArgBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil {
		return nil, fmt.Errorf("SellOrderLockFromOutputElements: timeoutSlots: %w", err)
	}
	return &SellOrderLock{
		SellerHolderID: holder,
		Tag:            tag,
		Price:          price,
		TimeoutSlots:   timeoutSlots,
	}, nil
}

func registerSellOrderLock(lib *Library) {
	lib.mustRegisterConstraint(SellOrderLockName, 2, func(data []byte) (Constraint, error) {
		return &lockKindMarker{name: SellOrderLockName, bytecode: bytes.Clone(data)}, nil
	})
}

// -----------------------------------------------------------------------------
// BuyOrderLock
// -----------------------------------------------------------------------------

// BuyOrderLock locks a UTXO that offers to buy `Amount` native tokens of `Tag`
// at `Price` base tokens per token. The order UTXO carries enough base tokens
// to cover Amount*Price plus the recipient receipt's storage deposit.
//
// Index-value tuple at output element index 1: [BuyerHolderID, "ORDR"||Tag||0x00].
// The lock bytecode at element 2 is buyOrder(Amount, Price, TimeoutSlots).
type BuyOrderLock struct {
	BuyerHolderID base.HolderID
	Tag           base.ChainID
	Amount        uint64
	Price         uint64
	TimeoutSlots  uint32
}

const buyOrderTemplate = BuyOrderLockName + "(z64/%d, z64/%d, z32/%d)"

func (l *BuyOrderLock) Name() string { return BuyOrderLockName }

func (l *BuyOrderLock) Source() string {
	return fmt.Sprintf(buyOrderTemplate, l.Amount, l.Price, l.TimeoutSlots)
}

func (l *BuyOrderLock) LockBytecode() []byte { return mustBinFromSource(l.Source()) }

func (l *BuyOrderLock) IndexValues() [][]byte {
	return [][]byte{l.BuyerHolderID[:], dexOrderEntry(l.Tag, dexOrderEntrySideBuyByte)}
}

func (l *BuyOrderLock) String() string {
	return fmt.Sprintf("buyOrder(buyer=%s, tag=%s, amount=%d, price=%d, timeoutSlots=%d)",
		hex.EncodeToString(l.BuyerHolderID[:]), l.Tag.String(), l.Amount, l.Price, l.TimeoutSlots)
}

// BuyOrderLockFromOutputElements rebuilds a BuyOrderLock from a UTXO's
// position-1 (index-values) and position-2 (lock bytecode) elements.
func BuyOrderLockFromOutputElements(indexValuesBytes, lockBytecode []byte, lib *Library) (*BuyOrderLock, error) {
	tag, holder, err := parseOrderIndexValues(indexValuesBytes, dexOrderEntrySideBuyByte)
	if err != nil {
		return nil, fmt.Errorf("BuyOrderLockFromOutputElements: %w", err)
	}
	sym, _, args, err := lib.ParseBytecodeOneLevel(lockBytecode, 3)
	if err != nil {
		return nil, fmt.Errorf("BuyOrderLockFromOutputElements: %w", err)
	}
	if sym != BuyOrderLockName {
		return nil, fmt.Errorf("BuyOrderLockFromOutputElements: expected %s, got %s", BuyOrderLockName, sym)
	}
	amount, err := uint64FromArgBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, fmt.Errorf("BuyOrderLockFromOutputElements: amount: %w", err)
	}
	price, err := uint64FromArgBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil {
		return nil, fmt.Errorf("BuyOrderLockFromOutputElements: price: %w", err)
	}
	timeoutSlots, err := uint32FromArgBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return nil, fmt.Errorf("BuyOrderLockFromOutputElements: timeoutSlots: %w", err)
	}
	return &BuyOrderLock{
		BuyerHolderID: holder,
		Tag:           tag,
		Amount:        amount,
		Price:         price,
		TimeoutSlots:  timeoutSlots,
	}, nil
}

func registerBuyOrderLock(lib *Library) {
	lib.mustRegisterConstraint(BuyOrderLockName, 3, func(data []byte) (Constraint, error) {
		return &lockKindMarker{name: BuyOrderLockName, bytecode: bytes.Clone(data)}, nil
	})
}

// -----------------------------------------------------------------------------
// randomizeConsumption — an additive constraint, not a Lock kind. The order's
// own lock at position 2 stays sellOrder/buyOrder; this constraint sits at
// the next free position and gates consumption with a per-slot lottery.
// -----------------------------------------------------------------------------

const randomizeConsumptionTemplate = RandomizeConsumptionName + "(z16/%d)"

// RandomizeConsumptionBytecode compiles the constraint bytecode for a given N.
// Returns an error if N is out of the [DexRandomizeMinN, DexRandomizeMaxN] range.
func RandomizeConsumptionBytecode(n uint8) ([]byte, error) {
	if n < DexRandomizeMinN || n > DexRandomizeMaxN {
		return nil, fmt.Errorf("RandomizeConsumptionBytecode: N=%d out of [%d, %d]", n, DexRandomizeMinN, DexRandomizeMaxN)
	}
	return mustBinFromSource(fmt.Sprintf(randomizeConsumptionTemplate, n)), nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// parseOrderIndexValues decodes the standard [issuerHolderID, "ORDR"||tag||side]
// index-values tuple, validating the side byte matches `expectedSide`.
func parseOrderIndexValues(indexValuesBytes []byte, expectedSide byte) (base.ChainID, base.HolderID, error) {
	var tag base.ChainID
	var holder base.HolderID
	values, err := IndexValuesFromBytes(indexValuesBytes)
	if err != nil {
		return tag, holder, err
	}
	if len(values) < 2 {
		return tag, holder, fmt.Errorf("expected at least 2 index-values entries, got %d", len(values))
	}
	if len(values[0]) != 32 {
		return tag, holder, fmt.Errorf("index-values[0] must be a 32-byte holder ID, got %d bytes", len(values[0]))
	}
	// entry shape: "ORDR" (4) || tag (ChainIDLength) || side (1)
	const prefixLen = 4
	entryLen := prefixLen + base.ChainIDLength + 1
	sideIdx := prefixLen + base.ChainIDLength
	if len(values[1]) != entryLen {
		return tag, holder, fmt.Errorf("index-values[1] must be a %d-byte ORDR entry, got %d bytes", entryLen, len(values[1]))
	}
	if string(values[1][:prefixLen]) != dexOrderBookPrefix {
		return tag, holder, fmt.Errorf("index-values[1] must start with %q", dexOrderBookPrefix)
	}
	if values[1][sideIdx] != expectedSide {
		return tag, holder, fmt.Errorf("index-values[1] side byte = 0x%02x, expected 0x%02x", values[1][sideIdx], expectedSide)
	}
	copy(holder[:], values[0])
	copy(tag[:], values[1][prefixLen:sideIdx])
	return tag, holder, nil
}

// uint64FromArgBytes decodes a z-encoded uint64 (≤8 BE bytes).
func uint64FromArgBytes(b []byte) (uint64, error) {
	if len(b) > 8 {
		return 0, fmt.Errorf("got %d bytes, want ≤ 8", len(b))
	}
	var v uint64
	for _, by := range b {
		v = (v << 8) | uint64(by)
	}
	return v, nil
}

// uint32FromArgBytes decodes a z-encoded uint32 (≤4 BE bytes).
func uint32FromArgBytes(b []byte) (uint32, error) {
	if len(b) > 4 {
		return 0, fmt.Errorf("got %d bytes, want ≤ 4", len(b))
	}
	padded := make([]byte, 4)
	copy(padded[4-len(b):], b)
	return binary.BigEndian.Uint32(padded), nil
}
