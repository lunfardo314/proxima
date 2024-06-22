package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/proxima/util"
)

// This file contains definitions of the inflation-related functions in EasyFL (on-ledger) and on IdentityData
// constants. The two must exactly match each other

// CalcChainInflationAmount interprets EasyFl formula
func (id *IdentityData) CalcChainInflationAmount(inTs, outTs Time, inAmount, delayed uint64) uint64 {
	src := fmt.Sprintf("calcChainInflationAmount(%s,%s,u64/%d, u64/%d)", inTs.Source(), outTs.Source(), inAmount, delayed)
	res, err := L().EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(res)
}

// BranchInflationBonusFromRandomnessProof makes uint64 in the range from 0 to BranchInflationBonusBase (incl)
func (id *IdentityData) BranchInflationBonusFromRandomnessProof(proof []byte) uint64 {
	src := fmt.Sprintf("maxBranchInflationAmountFromVRFProof(0x%s)", hex.EncodeToString(proof))
	res, err := L().EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(res)
}

// inflationFunctionsSource is a EasyFL source (inflationAmount function) of on-ledger calculation of inflation amount
// It must be equivalent to the direct calculation. It is covered in tests/inflation_test.go
const inflationFunctionsSource = `
// $0 - diff ticks between transaction timestamp and input timestamp
// $1 - amount on the chain input (no branch bonus)
func _insideInflationOpportunityWindow : 
and(
   lessOrEqualThan(
	   div(
		  $0,
		  ticksPerSlot64
	   ),
       constChainInflationOpportunitySlots
   ),
   lessThan(
       $0,
       div(
           0xffffffffffffffff, // MaxUint64
           $1
       )
   )
)

// $0 - timestamp of the previous chain output (not necessarily a predecessor)
// $1 - timestamp of the transaction (and of the output)
// $2 - amount on the chain input
// $3 - delayed inflation amount
// result: (dtInTicks * amountAtTheBeginning) / inflationFractionPerTick
func calcChainInflationAmount : 
if(
	_insideInflationOpportunityWindow(ticksBefore($0, $1), $2),
	add(
		div(
		   mul(
			  ticksBefore($0, $1), 
			  $2
		   ), 
		   constChainInflationPerTickFraction
	   ),
	   $3
	),
   u64/0
)

// $0 - VRF proof taken from the inflation constraint
func maxBranchInflationAmountFromVRFProof :
if(
	isZero(len($0)),
	u64/0,
	mod(
	   slice(blake2b($0), 0, 7),
	   add(constBranchInflationBonusBase, u64/1)
	)	
)
`
