package ledger

import (
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// CalcChainInflationAmountOneSlot calculates inflation for one slot. Inflation cannot be bigger than one slot.
// This makes token holder move the output every slot to earn maximum inflation
func (lib *Library) CalcChainInflationAmountOneSlot(inSlot base.Slot, inCoverage uint64) uint64 {
	return inCoverage / (lib.ID.SlotInflationFraction + uint64(inSlot))
}

func (lib *Library) BranchInflationBonusBase() uint64 {
	res, err := lib.EvalFromSource(nil, "constBranchInflationBonusBase")
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

func (lib *Library) BranchInflationBonusDirect(proof []byte) uint64 {
	return RandomFromSeed(proof, lib.BranchInflationBonusBase()) + 1
}

// BranchInflationBonusFromRandomnessProof makes uint64 in the range from 0 to BranchInflationBonusBase (incl)
//func (lib *Library) BranchInflationBonusFromRandomnessProof(proof []byte) uint64 {
//	src := fmt.Sprintf("branchInflationBonusFromRandomnessProof(0x%s)", hex.EncodeToString(proof))
//	res, err := lib.EvalFromSource(nil, src)
//	util.AssertNoError(err)
//	return binary.BigEndian.Uint64(res)
//}

const _inflationFunctionsSource = `

// aux value
// $0 predecessor timestamp bytes
// $1 successor timestamp bytes
func _adjustedDiffSlots :
	add(
       sub(first4Bytes($1), first4Bytes($0)),
       if (isTimestampBytesOnSlotBoundary($0), u64/1, u64/0)
    )

// $0 - ledger time (timestamp bytes) of the predecessor
// $1 - amount on predecessor
func _baseInflation : div($1, add(div(constInitialSupply,constSlotInflationBase), first4Bytes($0)))

// $0 - ledger time (timestamp) of the predecessor
// $1 - adjusted diff slots
// $2 - amount on predecessor
func _calcChainInflationAmount : 
    if(
       lessThan(constLinearInflationSlots, $1),
       mul(constLinearInflationSlots, _baseInflation($0, $2)),
       mul($1, _baseInflation($0, $2))
    )

// $0 - ledger time (timestamp) of the predecessor
// $1 - ledger time (timestamp) of the successor
// $2 - amount on predecessor
func calcChainInflationAmount : 
    if(
        not(lessThan($0, $1)),
        !!!calcChainInflationAmount_failed_wrong_timestamps,
   	    if(
           isTimestampBytesOnSlotBoundary($1),
           u64/0,
           _calcChainInflationAmount($0, _adjustedDiffSlots($0, $1), $2)
        )
    )

// $0 - VRF proof
// returns 8 bytes of big-endian uint64 value in the range from 1 to constBranchInflationBonusBase (inclusive)
// taken from the VRF proof (ED25519 signature). Output of this function is verifiable randomness
func branchInflationBonusFromRandomnessProof :
    add(randomFromSeed($0, constBranchInflationBonusBase), 1)
`
