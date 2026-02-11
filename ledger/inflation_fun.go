package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
)

func (lib *Library) ChainInflationMultiStepOriginal(amount uint64, inSlot, forSlots uint32) uint64 {
	src := fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)", amount, inSlot, forSlots)
	resBin, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(resBin)
}

func ChainInflationMultiStep(amount uint64, inSlot, forSteps uint32) uint64 {
	return uint64(forSteps) * (amount / (L(inSlot).MinimumInflatableAmount0 + uint64(inSlot)))
}

// AdjustedAmount calculates amount adjusted to the maximum inflation.
// I.e. adjusted amount A as a result to maximal inflation in 'slot' slots reaches 'amount'
// If amount == totalSupply, then  adjusted amount == initialSupply
// TODO experimental
//   - consider name 'real supply/amount/balance'
//   - consider adjustment of the branch inflation bonus
func AdjustedAmount(amount uint64, slot uint32) uint64 {
	lib := L(slot)
	return lib.MinimumInflatableAmount0 * (amount / (lib.MinimumInflatableAmount0 + uint64(slot)))
}

func ChainInflationOneSlot(amount uint64, inSlot uint32) uint64 {
	return ChainInflationMultiStep(amount, inSlot, 1)
}

func (lib *Library) BranchInflationBonusBaseFromSource() uint64 {
	res, err := lib.EvalFromSource(nil, "constBranchInflationBonusBase")
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

// BranchInflationBonus calculates the inflation bonus for a branch using the given proof.
// Uses the library for the specified slot.
func BranchInflationBonus(proof []byte, slot uint32) uint64 {
	return RandomFromSeed(proof, L(slot).BranchInflationBonusBase) + 1
}

func MinimumInflatableAmount(slot uint32) uint64 {
	lib := L(slot)
	return lib.MinimumInflatableAmount0 + ChainInflationMultiStep(lib.MinimumInflatableAmount0, 0, slot)
}
