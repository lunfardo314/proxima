package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
)

const amountSource = `
// $0 - amount up to 8 bytes big-endian. Will be expanded to 8 bytes by padding
func amount: 
   require(
       // constraint must be at index 0 and arg0 must no more than 8 byte-long 
       and(equal(selfBlockIndex,0), lessOrEqualThan(len($0), u64/8)), 
       !!!amount_constraint_must_be_at_index_0_and_len_arg0<=8
   )

// $0 path to output
// Returns amount value 8 bytes from the output at path given in $0
func amountValueByOutputPath : uint8Bytes(parseInlineDataArgument(atPath(concat($0, amountConstraintIndex)), #amount,0))

func selfAmountValue: amountValueByOutputPath(selfOutputPath)

// $0 number of output bytes
func storageDeposit : mul(constVBCost16,$0)

// enforces storage deposit
func enforceMinimumStorageDeposit: 
	require(
		not(lessThan(selfAmountValue, storageDeposit(len(selfOutputBytes)))),
		!!!amount_on_output_is_smaller_than_allowed_minimum
	)
`

const (
	AmountConstraintName = "amount"
	amountTemplate       = AmountConstraintName + "(z64/%d)"
)

type Amount uint64

func (a Amount) Name() string {
	return AmountConstraintName
}

// arg 0 is trimmed-prefix big-endian bytes uin64

func (a Amount) Source() string {
	return fmt.Sprintf(amountTemplate, uint64(a))
}

func (a Amount) Bytes() []byte {
	return mustBinFromSource(a.Source())
}

func (a Amount) String() string {
	//return a.Source()
	return fmt.Sprintf("%s(%s)", AmountConstraintName, util.Th(uint64(a)))
}

func NewAmount(a uint64) Amount {
	return Amount(a)
}

func registerAmountConstraint(lib *Library) {
	lib.mustRegisterConstraint(AmountConstraintName, 1, func(data []byte) (Constraint, error) {
		return AmountFromBytes(data)
	}, initTestAmountConstraint)
}

func initTestAmountConstraint() {
	example := NewAmount(1337)
	sym, _, args, err := L().ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	amountBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(sym == AmountConstraintName && len(amountBin) <= 8, "'amount' consistency check 1 failed")
	value, err := easyfl_util.Uint64FromBytes(amountBin)
	util.AssertNoError(err)
	util.Assertf(value == 1337, "amount' consistency check 2 failed")
}

func AmountFromBytes(data []byte) (Amount, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data)
	if err != nil {
		return 0, err
	}
	if sym != AmountConstraintName {
		return 0, fmt.Errorf("not an 'amount' constraint")
	}
	amountBin := easyfl.StripDataPrefix(args[0])
	ret, err := easyfl_util.Uint64FromBytes(amountBin)
	if err != nil {
		return 0, err
	}
	return Amount(ret), nil
}

func (a Amount) Amount() uint64 {
	return uint64(a)
}
