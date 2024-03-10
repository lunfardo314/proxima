package ledger

import (
	"math"

	"github.com/lunfardo314/proxima/util"
)

// TODO randomize branch inflation.
//  For example <branch inflation value> <= <nonce> mod 5000, where nonce is hash(txBytes), some VDF value or similar
//  with few ticks delay on a reasonable machine.
//  Sequencer would need to mine bigger inflation values, which will costs time to issue transactions

// InflationAmount is calculation of inflation amount directly from ledger identity constants
func (id *IdentityData) InflationAmount(inTs, outTs Time, inAmount uint64) uint64 {
	if outTs.IsSlotBoundary() {
		// for branch transactions fixed inflation
		return id.BranchBonusBase
	}
	return id.ChainInflationAmount(inTs, outTs, inAmount)
}

func (id *IdentityData) _epochFromGenesis(slot Slot) uint64 {
	return uint64(slot) / uint64(id.SlotsPerHalvingEpoch)
}

func (id *IdentityData) _halvingEpoch(epochFromGenesis uint64) uint64 {
	if epochFromGenesis < uint64(id.NumHalvingEpochs) {
		return epochFromGenesis
	}
	return uint64(id.NumHalvingEpochs)
}

func (id *IdentityData) InflationFractionBySlot(slotIn Slot) uint64 {
	return id.ChainInflationPerTickFractionBase * (1 << id._halvingEpoch(id._epochFromGenesis(slotIn)))
}

// ChainInflationAmount mocks inflation amount formula from the constraint library
// Safe arithmetics!
func (id *IdentityData) ChainInflationAmount(inTs, outTs Time, inAmount uint64) uint64 {
	diffTicks := DiffTicks(outTs, inTs)
	util.Assertf(diffTicks > 0, "ChainInflationAmount: wrong timestamps")
	util.Assertf(inAmount > 0, "ChainInflationAmount: inAmount > 0")

	if id._insideInflationOpportunityWindow(diffTicks, inAmount) {
		util.Assertf(uint64(diffTicks) <= math.MaxUint64/inAmount, "ChainInflationAmount: arithmetic overflow: diffTicks: %d, inAmount: %d",
			diffTicks, inAmount)
		return uint64(diffTicks) * inAmount / id.InflationFractionBySlot(inTs.Slot())
	}
	// non-zero inflation is only within the window of opportunity to disincentivize "lazy whales" or very big amounts
	return 0
}

// _insideInflationOpportunityWindow must be exactly the same as in EasyFL function _insideInflationOpportunityWindow
func (id *IdentityData) _insideInflationOpportunityWindow(diffTicks int64, inAmount uint64) bool {
	// default window is 1299 ticks, assuming 12 slots window and 100 tick per slot
	// to prevent overflow, we also check the situation when amount is very big.
	// by default, maximum amount which can be inflated will be MaxUint64 / 1299
	// it means diffTicks * inAmount overflows uint64. Initial amount is too small, cannot be inflated
	return uint64(diffTicks)/uint64(id.TicksPerSlot()) <= id.ChainInflationOpportunitySlots &&
		uint64(diffTicks) < math.MaxUint64/inAmount
}

// inflationSource is a EasyFL source (inflationAmount function) of on-ledger calculation of inflation amount
// It must be equivalent to the direct calculation. It is covered in tests/inflation_test.go
const inflationSource = `
// $0 -  slot of the chain input as u64
func epochFromGenesis : div64( $0, constSlotsPerLedgerEpoch )

// $0 -  epochFromGenesis
func halvingEpoch :
	if(
		lessThan($0, constHalvingEpochs),
        $0,
        constHalvingEpochs
	)

// $0 slot of the chain input as u64
// result - inflation fraction corresponding to that year (taking into account halving) 
func inflationFractionBySlot :
    mul64(
        constChainInflationFractionBase, 
        lshift64(u64/1, halvingEpoch(epochFromGenesis($0)))
    )

// $0 - diff ticks between transaction timestamp and input timestamp
// $1 - amount on the chain input (no branch bonus)
func _insideInflationOpportunityWindow : 
and(
   lessOrEqualThan(
	   div64(
		  $0,
		  ticksPerSlot64
	   ),
       constChainInflationOpportunitySlots
   ),
   lessThan(
       $0,
       div64(
           0xffffffffffffffff, // MaxUint64
           $1
       )
   )
)

// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
// $2 - amount on the chain input
// result: (dt * amount)/inflationFraction
func chainInflationAmount : 
if(
    _insideInflationOpportunityWindow(ticksBefore($0, $1), $2),
	div64(
	   mul64(
		  ticksBefore($0, $1), 
		  $2
	   ), 
	   inflationFractionBySlot( concat(u32/0, timeSlotFromTimeSlotPrefix(timeSlotPrefix($0))) )
   ),
   u64/0
)

// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
// $2 - amount on the chain input
func inflationAmount : 
if(
	isZero(timeTickFromTimestamp($1)),
    constInitialBranchBonus,  // fixed amount on branch
    chainInflationAmount($0, $1, $2)
)
`
