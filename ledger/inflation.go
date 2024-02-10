package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

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
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
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

func NewInflationConstraintUnlockParams(successorConstraintIndex byte) []byte {
	return []byte{successorConstraintIndex}
}

func initInflationConstraint() {
	L().MustExtendMany(fmt.Sprintf(inflationConstantsTemplate, InflationAmountBranchFixed, InflationAmountPerChainFraction))
	L().MustExtendMany(inflationConstraintSource)
	// sanity check
	example := NewInflationConstraint(125, 1337)
	exampleBin := example.Bytes()
	util.Assertf(example.ChainConstraintIndex == 125, "extend 'inflation' failed")
	util.Assertf(example.Amount == 1337, "extend 'inflation' failed")
	exampleBack, err := InflationConstraintFromBytes(exampleBin)
	util.AssertNoError(err)
	util.Assertf(example.ChainConstraintIndex == exampleBack.ChainConstraintIndex, "extend 'inflation' failed")
	util.Assertf(example.Amount == exampleBack.Amount, "extend 'inflation' failed")

	prefix, err := L().ParseBytecodePrefix(exampleBin)
	util.AssertNoError(err)

	registerConstraint(InflationConstraintName, prefix, func(data []byte) (Constraint, error) {
		return InflationConstraintFromBytes(data)
	})
}

const inflationConstantsTemplate = `
func branchInflationAmount : u64/%d
func chainInflationFraction : u64/%d
`

const inflationConstraintSource = `
// $0 inflation amount
func requireCorrectInflationAmount :
	if( 
       isBranchTransaction,
	   require(
           or(
              isZero($0), 
              equal($0, branchInflationAmount)
           ), 
           !!!wrong_inflation_amount_on_branch
       ),
       require(
           // must be (selfAmountValue-inflation) / fraction == inflation . Here '/' is integer division, floor(a/b)
           // i.e. output amount must include inflation
           or(
               isZero($0), 
               equal( $0, div64(sub64(selfAmountValue, $0), chainInflationFraction)) 
           ), 
           !!!wrong_inflation_amount
       )
	)

// $0 index of the chain constraint on the same output
// enforce the chain is not an origin, nor the end of it
func requireSiblingChainConstraint : requireChainTransition(unwrapBytecodeArg(selfSiblingConstraint($0), #chain, 0))

// $0 - chain successor output index
// $1 - inflation constraint index
func requireInflationSuccessor : and(
    require(
       // next in the same slot must be zero inflation
       isZero(unwrapBytecodeArg(producedConstraintByIndex(concat($0,$1)), selfBytecodePrefix, 1)),
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
// $1 - u64 inflation amount. The amount on the output must be *including* inflation
func _inflation : or(
    and(
        selfIsProducedOutput,
		requireSiblingChainConstraint($0),
		requireCorrectInflationAmount($1)
    ),
	and(
        selfIsConsumedOutput,
        or(
            // branch does not enforce next inflation
            branchFlagsON(inputIDByIndex(selfOutputIndex)),
            // cross-slot does not enforce next inflation
            not(equal(timeSlotOfInputByIndex(selfOutputIndex), txTimeSlot)),
            // on the same slot requires valid inflation successor
            requireInflationSuccessor(
                byte(selfSiblingUnlockBlock($0), 0), // take index of chain successor output
                selfUnlockParameters // must be one byte of inflation constraint index on successor
            )
        )
    )
)

// $0 - sibling chain constraint index
// $1 - u64 inflation amount
// unlock data: one byte with successor inflation constraint index on successor chain output
func inflation : and(
    mustSize($0, 1),
    mustSize($1, 8),
    _inflation($0, $1)
)
`
