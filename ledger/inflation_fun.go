package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
)

func (lib *Library) ChainInflationOriginal(amount uint64, inSlot, forSlots uint32) uint64 {
	src := fmt.Sprintf("chainInflation(u64/%d, u64/%d, u64/%d)", amount, inSlot, forSlots)
	resBin, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(resBin)
}

func ChainInflation(amount uint64, inSlot, forSlots uint32) uint64 {
	lib := L(inSlot)
	return uint64(forSlots) * (amount / (lib.MinimumInflatableAmount0 + uint64(inSlot)))
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
	return ChainInflation(amount, inSlot, 1)
}

func (lib *Library) BranchInflationBonusBaseFromSource() uint64 {
	res, err := lib.EvalFromSource(nil, "constBranchInflationBonusBase")
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

// BranchInflationBonus calculates the inflation bonus for a branch using the given proof.
// Uses the library for the specified slot.
func BranchInflationBonus(proof []byte, slot uint32) uint64 {
	lib := L(slot)
	return RandomFromSeed(proof, lib.BranchInflationBonusBase) + 1
}

func MinimumInflatableAmount(slot uint32) uint64 {
	lib := L(slot)
	return lib.MinimumInflatableAmount0 + ChainInflation(lib.MinimumInflatableAmount0, 0, slot)
}
