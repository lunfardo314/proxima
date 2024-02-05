package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

var correctInflationAmountSource = `func correctInflationAmount : 
if( isBranchTransaction,
   u64/%d,
   div64($0, u64/%d)       
)
`

const inflationConstraintSource = `

// $0 index of the chain constraint on the same output
// enforce the chain is not an origin, nor the end of it
func requireSiblingChainConstraint : requireChainTransition(unwrapBytecodeArg(selfSiblingConstraint($0), #chain, 0))

// $0 is inflation amount data. It must be u8/0 or u64/
func requireValidInflationAmount : require(
    or(
       equal($0, 0),
       equal($0, correctInflationAmount(selfAmountValue))
    ),
    !!!wrong_inflation_amount
)

// $0 - chain successor output index
// $1 - inflation constraint index
func requireInflationSuccessor : and(
    require(
       equal(unwrapBytecodeArg(producedConstraintByIndex(concat($0,$1)), #chain, 1), 0),
       !!!wrong_inflation_constraint_in_the_successor
    ),
    require(
       lessOrEqualThan(
           selfAmountValue,
           amountValue(producedOutputByIndex($0))
       ),
       !!!amount_should_not_decrease_on_inflated_output_successor
    )
) 


// $0 - sibling chain constraint index
// $1 - u64 inflation amount or u8/0 
// unlock data: one byte slice with successor inflation constraint index
func inflation : or(
    and(
        selfIsProducedOutput,
		requireSiblingChainConstraint($0),
		requireValidInflationAmount($1)
    ),
	and(
        selfIsConsumedOutput,
		if(
            equal(timeSlotOfInputByIndex(selfOutputIndex), txTimeSlot),
               // on the same slot. Next must be with nil amount and not shrinking amount
            requireInflationSuccessor(
                byte(selfSiblingUnlockBlock($0) , 0), 
                selfUnlockParameters // must be one byte of index
            ), 
               // cross slot - do not enforce
            true
        )
    )
)

func inflationRepeat : inflation($0, 0)
`

type (
	InflationConstraint struct {
		Amount               uint64
		ChainConstraintIndex byte
	}
)

const (
	InflationAmountBranchFixed      = uint64(1_000)
	InflationAmountPerChainFraction = 1_000_000

	InflationConstraintName     = "inflation"
	inflationConstraintTemplate = InflationConstraintName + "(%d, u64/%d)"
)

func (i *InflationConstraint) Name() string {
	return InflationConstraintName
}

func (i *InflationConstraint) source() string {
	return fmt.Sprintf(inflationConstraintTemplate, i.ChainConstraintIndex, i.Amount)
}

func (i *InflationConstraint) Bytes() []byte {
	return mustBinFromSource(i.source())
}

func (i *InflationConstraint) String() string {
	return fmt.Sprintf("%s(%d, %s)", InflationConstraintName, i.ChainConstraintIndex, util.GoTh(i.Amount))
}

func NewInflationConstraint(chainConstraintIndex byte, amount uint64) *InflationConstraint {
	return &InflationConstraint{
		Amount:               amount,
		ChainConstraintIndex: chainConstraintIndex,
	}
}

func InflationConstraintFromBytes(data []byte) (*InflationConstraint, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != InflationConstraintName {
		return nil, fmt.Errorf("not a 'inflation' constraint")
	}
	chainConstraintIdxBin := easyfl.StripDataPrefix(args[0])
	if len(chainConstraintIdxBin) != 1 {
		return nil, fmt.Errorf("inflation contraint: wrong data length 1st arg")
	}
	amountBin := easyfl.StripDataPrefix(args[1])
	if len(amountBin) != 8 {
		return nil, fmt.Errorf("inflation contraint: wrong data length 2nd arg")
	}
	return &InflationConstraint{
		Amount:               binary.BigEndian.Uint64(amountBin),
		ChainConstraintIndex: chainConstraintIdxBin[0],
	}, nil
}

func InflationConstraintUnlockData(successorConstraintIndex byte) []byte {
	return []byte{successorConstraintIndex}
}

func initInflationConstraint() {
	easyfl.MustExtendMany(fmt.Sprintf(correctInflationAmountSource, InflationAmountBranchFixed, InflationAmountPerChainFraction))
	easyfl.MustExtendMany(inflationConstraintSource)
	// sanity check
	example := NewInflationConstraint(125, 1337)
	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 2)
	util.AssertNoError(err)
	chainConstrIdx := easyfl.StripDataPrefix(args[0])
	util.Assertf(sym == InflationConstraintName && len(chainConstrIdx) == 1 && chainConstrIdx[0] == 125, "'inflation' consistency check failed")
	amountBin := easyfl.StripDataPrefix(args[1])
	util.Assertf(len(amountBin) == 8 && binary.BigEndian.Uint64(amountBin) == 1337, "'inflation' consistency check failed")

	registerConstraint(InflationConstraintName, prefix, func(data []byte) (Constraint, error) {
		return InflationConstraintFromBytes(data)
	})
}
