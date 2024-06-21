package ledger

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// This file contains definitions of the inflation-related functions in EasyFL (on-ledger) and on IdentityData
// constants. The two must exactly match each other

// ChainInflationAmount interprets EasyFl formula
func (id *IdentityData) ChainInflationAmount(inTs, outTs Time, inAmount uint64) uint64 {
	src := fmt.Sprintf("maxChainInflationAmount(%s,%s,u64/%d)", inTs.Source(), outTs.Source(), inAmount)
	res, err := L().EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(res)
}

// ChainInflationAmountOld mocks inflation amount formula from the constraint library
// Deprecated: use interpreted version
func (id *IdentityData) ChainInflationAmountOld(inTs, outTs Time, inAmount uint64) uint64 {
	if outTs.IsSlotBoundary() {
		return 0
	}
	diffTicks := DiffTicks(outTs, inTs)
	util.Assertf(diffTicks > 0, "ChainInflationAmount: wrong timestamps")
	util.Assertf(inAmount > 0, "ChainInflationAmount: inAmount > 0")

	if id._insideInflationOpportunityWindow(diffTicks, inAmount) {
		if uint64(diffTicks) <= math.MaxUint64/inAmount {
			// safe arithmetics check
			return (uint64(diffTicks) * inAmount) / id.ChainInflationPerTickFraction
		}
		// TODO inflation 0 due to overflow
		return 0
	}
	// non-zero inflation is only within the window of opportunity to disincentive-ize "lazy whales"
	return 0
}

// _insideInflationOpportunityWindow must be exactly the same as in EasyFL function _insideInflationOpportunityWindow
// Deprecated
func (id *IdentityData) _insideInflationOpportunityWindow(diffTicks int64, inAmount uint64) bool {
	// default window is 1299 ticks, assuming 12 slots window and 100 tick per slot
	// to prevent overflow, we also check the situation when amount is very big.
	// by default, maximum amount which can be inflated will be MaxUint64 / 1299
	// it means diffTicks * inAmount overflows uint64
	return uint64(diffTicks)/uint64(id.TicksPerSlot()) <= id.ChainInflationOpportunitySlots &&
		uint64(diffTicks) < math.MaxUint64/inAmount
}

// BranchInflationBonusFromRandomnessProof makes uint64 in the range from 0 to BranchInflationBonusBase (incl)
func (id *IdentityData) BranchInflationBonusFromRandomnessProof(proof []byte) uint64 {
	h := blake2b.Sum256(proof)
	n := binary.BigEndian.Uint64(h[0:8])
	return n % (id.BranchInflationBonusBase + 1)
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

// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
// $2 - amount on the chain input
// result: (dtInTicks * amountAtTheBeginning) / inflationFractionPerTick
func maxChainInflationAmount : 
if(
	isZero(timeTickFromTimestamp($1)),
    u64/0,  // 0 chain inflation on branch
	if(
		_insideInflationOpportunityWindow(ticksBefore($0, $1), $2),
		div(
		   mul(
			  ticksBefore($0, $1), 
			  $2
		   ), 
		   constChainInflationPerTickFraction
	   ),
	   u64/0
	)
) 

// $0 VRF proof taken from the inflation constraint
func maxBranchInflationAmountFromVRFProof :
mod(
   slice(blake2b($0), 0, 7),
   add(constBranchInflationBonusBase, u64/1)
)
`
