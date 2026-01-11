package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

const totalAmountSource = `
// $0 - total amount uint64 big-endian
// $0 must be equal to the total amount value in the transaction
func total: require(
	or(
		selfIsConsumedOutput,
        equalUint($0, txTotalProducedAmount)
	),
    !!!total_amount_constraint_failed
)
`

const (
	TotalAmountConstraintName = "total"
	totalAmountTemplate       = TotalAmountConstraintName + "(u64/%d)"
)

type TotalAmount uint64

func (a TotalAmount) Name() string {
	return TotalAmountConstraintName
}

func (a TotalAmount) Source() string {
	return fmt.Sprintf(totalAmountTemplate, uint64(a))
}

func (a TotalAmount) Bytes() []byte {
	return mustBinFromSource(a.Source())
}

func (a TotalAmount) String() string {
	return fmt.Sprintf("%s(%s)", TotalAmountConstraintName, util.Th(int(a)))
}

func NewTotalAmount(a uint64) TotalAmount {
	return TotalAmount(a)
}

func registerTotalAmountConstraint(lib *Library) {
	lib.mustRegisterConstraint(TotalAmountConstraintName, 1, func(data []byte) (Constraint, error) {
		return TotalAmountFromBytes(data)
	}, initTestTotalAmountConstraint)
}

func initTestTotalAmountConstraint() {
	// sanity check
	lib := L(base.MaxSlot)
	example := NewTotalAmount(1337)
	sym, _, args, err := lib.ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	totalAmountBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(sym == TotalAmountConstraintName && len(totalAmountBin) == 8 && binary.BigEndian.Uint64(totalAmountBin) == 1337, "'total' constraint consistency check failed")
}

// TotalAmountFromBytesAtSlot parses a TotalAmount constraint using the library for the given slot.
func TotalAmountFromBytesAtSlot(data []byte, slot uint32) (TotalAmount, error) {
	sym, _, args, err := L(slot).ParseBytecodeOneLevel(data)
	if err != nil {
		return 0, err
	}
	if sym != TotalAmountConstraintName {
		return 0, fmt.Errorf("not a 'total' constraint")
	}
	amountBin := easyfl.StripDataPrefix(args[0])
	if len(amountBin) != 8 {
		return 0, fmt.Errorf("wrong data length")
	}
	return TotalAmount(binary.BigEndian.Uint64(amountBin)), nil
}

// TotalAmountFromBytes parses a TotalAmount constraint using the latest library version.
// Deprecated: Use TotalAmountFromBytesAtSlot for parsing historical bytecode.
func TotalAmountFromBytes(data []byte) (TotalAmount, error) {
	return TotalAmountFromBytesAtSlot(data, base.MaxSlot)
}

func (a TotalAmount) Amount() uint64 {
	return uint64(a)
}
