package ledger

import (
	"bytes"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
)

const (
	InflationConstraintName = "inflation"
	// (0) chain constraint index, (1) inflation amount or randomness proof
	inflationConstraintTemplate = InflationConstraintName + "(z64/%d, %d)"
)

type InflationConstraint struct {
	// either it is nil, which means 0 inflation, or 8 bytes of chain inflation or VRF randomness proof (on the branch only)
	InflationAmount      uint64
	ChainConstraintIndex byte
}

func (i *InflationConstraint) Name() string {
	return InflationConstraintName
}

func (i *InflationConstraint) Bytes() []byte {
	return mustBinFromSource(i.Source())
}

func (i *InflationConstraint) String() string {
	return i.Source()
	//return fmt.Sprintf("%s(%s, 0x%s, %d, %d)",
	//	InflationConstraintName,
	//	util.Th(i.ChainInflation),
	//	hex.EncodeToString(i.VRFProof),
	//	i.ChainConstraintIndex,
	//	i.DelayedInflationIndex,
	//)
}

func (i *InflationConstraint) Source() string {
	return fmt.Sprintf(inflationConstraintTemplate, i.InflationAmount, i.ChainConstraintIndex)
}

func InflationConstraintFromBytes(data []byte) (*InflationConstraint, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != InflationConstraintName {
		return nil, fmt.Errorf("InflationConstraintFromBytes: not an inflation constraint script")
	}
	amountBin := easyfl.StripDataPrefix(args[0])
	amount, err := easyfl_util.Uint64FromBytes(amountBin)
	if err != nil {
		return nil, err
	}
	cci := easyfl.StripDataPrefix(args[1])
	if len(cci) != 1 || cci[0] == 0xff {
		return nil, fmt.Errorf("InflationConstraintFromBytes: wrong ChainConstraintIndex parameter")
	}
	return &InflationConstraint{
		ChainConstraintIndex: cci[0],
		InflationAmount:      amount,
	}, nil
}

func registerInflationConstraint(lib *Library) {
	lib.mustRegisterConstraint(InflationConstraintName, 2, func(data []byte) (Constraint, error) {
		return InflationConstraintFromBytes(data)
	}, initTestInflationConstraint)
}

func initTestInflationConstraint() {
	ic := InflationConstraint{
		InflationAmount:      13371337,
		ChainConstraintIndex: 5,
	}
	_, _, bytecode, err := L().CompileExpression(ic.Source())
	util.AssertNoError(err)

	util.Assertf(bytes.Equal(ic.Bytes(), bytecode), "bytes.Equal(ic.Bytes(), bytecode)")
}

const inflationConstraintSource = `
func _producedVRFProof : 
     parseInlineDataArgument(
        producedConstraintByIndex(concat(txStemOutputIndex, lockConstraintIndex)), 
        #stemLock, 
        1
     )

// $0 - chain predecessor input index
func _calcChainInflationAmountForPredecessor :
     calcChainInflationAmount(
	    timestampOfInputByIndex($0), 
        txTimestampBytes,
	    tokenBalanceByOutputPath(concat(pathToConsumedOutputs,$0)),
	 )

// inflation(<inflation amount>, <chain constraint index>)
// $0 - inflation amount (zero prefix trimmed up to 8 bytes).  
// $1 - sibling chain constraint index
//
func inflation : or(
	selfIsConsumedOutput, // not checked if consumed
	and(
  		selfIsProducedOutput,
        if(
           isBranchTransaction,
                   // branch tx. Enforce inflation is calculated from the VRF proof
           require(
                equal( uint8Bytes($0), branchInflationBonusFromRandomnessProof(_producedVRFProof) ),
                !!!invalid_branch_inflation_bonus
           ),
                   // not branch tx. Enforce valid chain inflation amount
           require(
	    		lessOrEqualThan(
                    uint8Bytes($0),
                    _calcChainInflationAmountForPredecessor(selfChainPredInputIndex($1))
			    ),
			    !!!invalid_chain_inflation_amount
		   )
        ),
    )
)
`
