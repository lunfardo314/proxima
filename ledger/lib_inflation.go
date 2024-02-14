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
		return id.InitialBranchBonus
	}
	return id.ChainInflationAmount(inTs, outTs, inAmount)
}

// ChainInflationAmount mocks inflation amount formula from the constraint library
func (id *IdentityData) ChainInflationAmount(inTs, outTs Time, inAmount uint64) uint64 {
	ticks := DiffTicks(outTs, inTs)
	util.Assertf(ticks > 0, "wrong timestamps")
	util.Assertf(inAmount > 0, "inAmount > 0")
	util.Assertf(uint64(ticks) <= math.MaxUint64/inAmount, "ChainInflationAmount: arithmetic overflow ")

	if id._insideInflationOpportunityWindow(inTs, outTs) {
		return uint64(ticks) * inAmount / id.InflationFractionBySlot(inTs.Slot())
	}
	// non-zero inflation is only within the window of opportunity
	// to disincentivize "lazy whales"
	return 0
}

func (id *IdentityData) _insideInflationOpportunityWindow(inTs, outTs Time) bool {
	ticks := DiffTicks(outTs, inTs)
	return uint64(ticks)/uint64(id.TicksPerSlot()) <= id.ChainInflationOpportunitySlots
}

func (id *IdentityData) _epochFromGenesis(slot Slot) uint64 {
	return uint64(slot) / uint64(id.SlotsPerLedgerEpoch)
}

func (id *IdentityData) _halvingEpoch(epochFromGenesis uint64) uint64 {
	if epochFromGenesis < uint64(id.ChainInflationHalvingEpochs) {
		return epochFromGenesis
	}
	return uint64(id.ChainInflationHalvingEpochs)
}

func (id *IdentityData) InflationFractionBySlot(slotIn Slot) uint64 {
	return id.ChainInflationPerTickFractionBase * (1 << id._halvingEpoch(id._epochFromGenesis(slotIn)))
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

// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
func insideInflationOpportunityWindow :
   lessOrEqualThan(
	   div64(
		  ticksBefore($0, $1),
		  ticksPerSlot64
	   ),
       constChainInflationOpportunitySlots
   )

// $0 - timestamp of the chain input
// $1 - timestamp of the transaction (and of the output)
// $2 - amount on the chain input (no branch bonus)
// result: (dt * amount)/inflationFraction
func chainInflationAmount : 
if(
    insideInflationOpportunityWindow($0, $1),
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
