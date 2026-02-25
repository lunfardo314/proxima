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

// BranchInflationBonusBase returns the maximum branch inflation bonus for the given slot.
func (lib *Library) BranchInflationBonusBase(slot uint32) uint64 {
	var slotBin [4]byte
	binary.BigEndian.PutUint32(slotBin[:], slot)
	res, err := lib.EvalFromSource(nil, "branchInflationBonusBase($0)", slotBin[:])
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

// BranchCoverageLowerBound returns the minimum sequencer coverage (tokenBalance + frozenCoverage)
// required to issue a branch transaction at the given slot.
func (lib *Library) BranchCoverageLowerBound(slot uint32) uint64 {
	var slotBin [4]byte
	binary.BigEndian.PutUint32(slotBin[:], slot)
	res, err := lib.EvalFromSource(nil, "branchCoverageLowerBound($0)", slotBin[:])
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

// BranchCoverageUpperBound returns the maximum sequencer coverage (tokenBalance + frozenCoverage)
// allowed to issue a branch transaction at the given slot.
func (lib *Library) BranchCoverageUpperBound(slot uint32) uint64 {
	var slotBin [4]byte
	binary.BigEndian.PutUint32(slotBin[:], slot)
	res, err := lib.EvalFromSource(nil, "branchCoverageUpperBound($0)", slotBin[:])
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

// BranchInflationBonus calculates the inflation bonus for a branch using the given proof.
// Uses the library for the specified slot.
func (lib *Library) BranchInflationBonus(proof []byte, slot uint32) uint64 {
	var slotBin [4]byte
	binary.BigEndian.PutUint32(slotBin[:], slot)
	res, err := lib.EvalFromSource(nil, "branchInflationBonus($0, $1)", proof, slotBin[:]) // TODO optimize precompile
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}
