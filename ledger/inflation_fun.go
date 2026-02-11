package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
)

func (lib *Library) ChainInflationMultiStep(amount uint64, inSlot, forSlots uint32) uint64 {
	src := fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)", amount, inSlot, forSlots)
	resBin, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(resBin)
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

func (lib *Library) ChainInflationOneSlot(amount uint64, inSlot uint32) uint64 {
	return lib.ChainInflationMultiStep(amount, inSlot, 1)
}

// BranchInflationBonus calculates the inflation bonus for a branch using the given proof.
// Uses the library for the specified slot.
func (lib *Library) BranchInflationBonus(proof []byte) uint64 {
	res, err := lib.EvalFromSource(nil, "branchInflationBonus($0)", proof) // optimize precompile
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}
