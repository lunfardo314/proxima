package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

const totalAmountSource = `
// $0 - total amount uint64 big-endian
// $0 must be equal to the total amount value in the transaction
func total: require(
	or(
		selfIsConsumedOutput,
        equal($0, txTotalProducedAmount),
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

func (a TotalAmount) source() string {
	return fmt.Sprintf(totalAmountTemplate, uint64(a))
}

func (a TotalAmount) Bytes() []byte {
	return mustBinFromSource(a.source())
}

func (a TotalAmount) String() string {
	return fmt.Sprintf("%s(%s)", TotalAmountConstraintName, util.GoTh(int(a)))
}

func NewTotalAmount(a uint64) TotalAmount {
	return TotalAmount(a)
}

func initTotalAmountConstraint() {
	easyfl.MustExtendMany(totalAmountSource)
	// sanity check
	example := NewTotalAmount(1337)
	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	totalAmountBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(sym == TotalAmountConstraintName && len(totalAmountBin) == 8 && binary.BigEndian.Uint64(totalAmountBin) == 1337, "'total' constraint consistency check failed")
	registerConstraint(TotalAmountConstraintName, prefix, func(data []byte) (Constraint, error) {
		return TotalAmountFromBytes(data)
	})
}

func TotalAmountFromBytes(data []byte) (TotalAmount, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data)
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

func (a TotalAmount) Amount() uint64 {
	return uint64(a)
}
