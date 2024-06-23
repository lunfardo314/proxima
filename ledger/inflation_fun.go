package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// This file contains definitions of the inflation-related functions in EasyFL (on-ledger) and on IdentityData
// constants. The two must exactly match each other

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
	return binary.BigEndian.Uint64(h[:7]) % (lib.ID.BranchInflationBonusBase + 1)
}

// InsideInflationOpportunityWindow returns if ticks and amount are inside inflation window
func (lib *Library) InsideInflationOpportunityWindow(diffTicks int, inAmount uint64) bool {
	src := fmt.Sprintf("_insideInflationOpportunityWindow(u64/%d, u64/%d)", diffTicks, inAmount)
	res, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return len(res) > 0
}

// inflationFunctionsSource is a EasyFL source (inflationAmount function) of on-ledger calculation of inflation amount
const inflationFunctionsSource = `
// $0 - diff ticks between transaction timestamp and input timestamp
// $1 - amount on the chain input (no branch bonus). If it is too big, no inflation
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
`
