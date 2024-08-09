package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// This file contains definitions of the inflation calculation functions in EasyFL (on-ledger)
// The Go functions interprets EasyFL function to guarantee consistent values

// CalcChainInflationAmount interprets EasyFl formula. Return chain inflation amount for given in and out ledger times,
// input amount of tokens and delayed
func (lib *Library) CalcChainInflationAmount(inTs, outTs Time, inAmount, delayed uint64) uint64 {
	delta := DiffTicks(outTs, inTs)
	src := fmt.Sprintf("calcChainInflationAmount(%s,u64/%d,u64/%d, u64/%d)", inTs.Source(), uint64(delta), inAmount, delayed)
	res, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(res)
}

func (lib *Library) UpperSupplyBound(inTs Time) uint64 {
	src := fmt.Sprintf("_upperSupplyBound(%s)", inTs.Source())
	res, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(res)
}

func (lib *Library) AmountFactor(inTs Time, inAmount uint64) uint64 {
	src := fmt.Sprintf("_amountFactor(%s,u64/%d)", inTs.Source(), inAmount)
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
func (lib *Library) InsideInflationOpportunityWindow(diffTicks int64) bool {
	src := fmt.Sprintf("_insideInflationOpportunityWindow(u64/%d)", diffTicks)
	res, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return len(res) > 0
}

const inflationFunctionsSource = `
// $0 - diff ticks between transaction timestamp and input timestamp
//
// Determines if diff ticks and amount fall inside inflation opportunity window
func _insideInflationOpportunityWindow : 
   lessOrEqualThan(
	   div(
		  $0,
		  ticksPerSlot64
	   ),
       constChainInflationOpportunitySlots
   )

// upperSupplyBound returns theoretical maximum of the total supply for the moment $0
// Real supply will be less. 
// The key point the upperSupplyBound is linear function of the ledger time 
// and does no depend on the real supply
// returns: initialSupply + chainInflationBase * ledgerTime + inflationBonusBase * ledgerNumSlots
//
// $0 ledger time (timestamp)
func _upperSupplyBound :
add( 
  add(
    constInitialSupply,
	mul(
       slotsSinceOrigin($0),
       constBranchInflationBonusBase
    )
  ),
  mul(
     constChainInflationPerTickBase,
     ticksSinceOrigin($0)
  )
)

// returns: max(10, upperSupplyBound / amount)
//
// $0 ledger time (timestamp)
// $1 amount 
func _amountFactor:
max(
  div(
    _upperSupplyBound($0),
    $1
  ),
  u64/10
)


// Returns chain inflation amount. In the inflation opportunity window it is equal to:
//   upperSupplyBound = initialSupply + constChainInflationPerTickBase * delta
//   amountFactor = max(10, upperSupplyBound / amount on predecessor)
//   delta - in ticks between chain outputs
//   inflation = (constChainInflationPerTickBase * delta) / amountFactor
// 
// division and amountFactor are needed for safe arithmetic without possibility of overflow

// $0 - ledger time (timestamp) of the predecessor
// $1 - delta in ticks (diff)
// $2 - amount on predecessor
// $3 - delayed amount
func calcChainInflationAmount :
add(
  if(
     _insideInflationOpportunityWindow($1),
     div(
        mul( constChainInflationPerTickBase, $1 ),
        _amountFactor( $0, $2 )
     ),
     u64/0
  ),
  $3
)
`
