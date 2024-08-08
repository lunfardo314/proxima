package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

const ()

// This file contains definitions of the inflation calculation functions in EasyFL (on-ledger)
// The Go functions interprets EasyFL function to guarantee consistent values

// CalcChainInflationAmount interprets EasyFl formula. Return chain inflation amount for given in and out ledger times,
// input amount of tokens and delayed
func (lib *Library) CalcChainInflationAmount(inTs, outTs Time, inAmount, delayed uint64) uint64 {
	src := fmt.Sprintf("calcChainInflationAmount(%s,%s,u64/%d, u64/%d)", inTs.Source(), outTs.Source(), inAmount, delayed)
	res, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(res)
}

// BranchInflationBonusFromRandomnessProof makes uint64 in the range from 0 to BranchInflationBonusBase (incl)
func (lib *Library) BranchInflationBonusFromRandomnessProof(proof []byte) uint64 {
	if len(proof) == 0 {
		return 0
	}
	h := blake2b.Sum256(proof)
	return binary.BigEndian.Uint64(h[:8]) % (lib.ID.BranchInflationBonusBase + 1)
}

// InsideInflationOpportunityWindow returns if ticks and amount are inside inflation opportunity window
// Outside inflation opportunity window mean 0 inflation
func (lib *Library) InsideInflationOpportunityWindow(diffTicks int, inAmount uint64) bool {
	src := fmt.Sprintf("_insideInflationOpportunityWindow(u64/%d, u64/%d)", diffTicks, inAmount)
	res, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return len(res) > 0
}

const inflationFunctionsSource = `
// $0 - diff ticks between transaction timestamp and input timestamp
// $1 - amount on the chain input (no branch bonus). If it is too big, no inflation
//
// Determines if diff ticks and amount fall inside inflation opportunity window
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

// upperSupplyBound returns theoretical maximum of the total supply for the moment $0
// Real supply will be less. 
// The key point the upperSupplyBound is linear function of the ledger time 
// and does no depend on the real supply
//
// $0 ledger time (timestamp)
func upperSupplyBound :
  add(
    constInitialSupply,
	mul(
       slotsSinceOrigin($0),
       constBranchInflationBonusBase
    ),
    mul(
       constChainInflationPerTickBase,
       ticksSinceOrigin($0)
    )
  )

// $0 ledger time (timestamp)
// $1 amount 
func _amountFactor:
  div(
    upperSupplyBound($0),
    $1
  )


// Returns chain inflation amount. In the inflation opportunity window it is equal to:
//   upperSupplyBound = initialSupply + constChainInflationPerTickBase * delta
//   amountFactor = upperSupplyBound / amount on predecessor
//   delta - in ticks between chain outputs
//   inflation = (constChainInflationPerTickBase * delta) / amountFactor
// 
// division and amountFactor are needed for safe arithmetic without possibility of overflow

// $0 - ledger time (timestamp) of the predecessor
// $1 - delta in ticks
// $2 - amount on predecessor
func calcChainInflationAmount : 
if(
   _insideInflationOpportunityWindow($1, $2),
   div(
      mul( constChainInflationPerTickBase, $1 ),
      _amountFactor( $0, $2 )
   ),
   u64/0
)
`
