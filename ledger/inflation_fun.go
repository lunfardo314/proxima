package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
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

// BranchCoverageLowerBound returns the minimum sequencer coverage (tokenBalance + frozenCoverage)
// required to issue a branch transaction at the given slot.
func (lib *Library) BranchCoverageLowerBound(slot uint32) uint64 {
	expr := lib.BranchCoverageLowerBoundPrecompiled.Load()
	if expr == nil {
		expr = lib.mustCompile("branchCoverageLowerBound($0)", 1)
		lib.BranchCoverageLowerBoundPrecompiled.Store(expr)
	}
	var slotBin [4]byte
	var res []byte
	binary.BigEndian.PutUint32(slotBin[:], slot)

	err := util.CatchPanicOrError(func() error {
		res = easyfl.EvalExpressionWithSlicePool(nil, nil, expr, slotBin[:])
		return nil
	})
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

// BranchCoverageUpperBound returns the maximum sequencer coverage (tokenBalance + frozenCoverage)
// allowed to issue a branch transaction at the given slot.
func (lib *Library) BranchCoverageUpperBound(slot uint32) uint64 {
	expr := lib.BranchCoverageUpperBoundPrecompiled.Load()
	if expr == nil {
		expr = lib.mustCompile("branchCoverageUpperBound($0)", 1)
		lib.BranchCoverageUpperBoundPrecompiled.Store(expr)
	}
	var slotBin [4]byte
	var res []byte
	binary.BigEndian.PutUint32(slotBin[:], slot)

	err := util.CatchPanicOrError(func() error {
		res = easyfl.EvalExpressionWithSlicePool(nil, nil, expr, slotBin[:])
		return nil
	})
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

// BranchInflationBonusBase returns the maximum branch inflation bonus for the given slot.
func (lib *Library) BranchInflationBonusBase(slot uint32) uint64 {
	expr := lib.BranchInflationBonusBasePrecompiled.Load()
	if expr == nil {
		expr = lib.mustCompile("branchInflationBonusBase($0)", 1)
		lib.BranchInflationBonusBasePrecompiled.Store(expr)
	}
	var slotBin [4]byte
	var res []byte
	binary.BigEndian.PutUint32(slotBin[:], slot)

	err := util.CatchPanicOrError(func() error {
		res = easyfl.EvalExpressionWithSlicePool(nil, nil, expr, slotBin[:])
		return nil
	})
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

// IsHealthyCoverageDelta returns true iff the branch is healthy under this library's
// healthy-coverage fraction, i.e. coverageDelta * denominator > 2 * supply * numerator.
// Delegates to the precompiled EasyFL function `healthyCoverageDelta(supply, covDelta)`
// so Go and on-chain (stemLock) checks share a single source of truth.
func (lib *Library) IsHealthyCoverageDelta(coverageDelta, supply uint64) bool {
	expr := lib.HealthyCoverageDeltaPrecompiled.Load()
	if expr == nil {
		expr = lib.mustCompile("healthyCoverageDelta($0, $1)", 2)
		lib.HealthyCoverageDeltaPrecompiled.Store(expr)
	}
	var supplyBin, covBin [8]byte
	binary.BigEndian.PutUint64(supplyBin[:], supply)
	binary.BigEndian.PutUint64(covBin[:], coverageDelta)

	var res []byte
	err := util.CatchPanicOrError(func() error {
		res = easyfl.EvalExpressionWithSlicePool(nil, nil, expr, supplyBin[:], covBin[:])
		return nil
	})
	util.AssertNoError(err)
	// EasyFL boolean: empty = false, non-empty = true.
	return len(res) > 0
}

// BranchInflationBonus calculates the inflation bonus for a branch using the given proof.
// Uses the library for the specified slot.
func (lib *Library) BranchInflationBonus(proof []byte, slot uint32) uint64 {
	expr := lib.BranchInflationBonusPrecompiled.Load()
	if expr == nil {
		expr = lib.mustCompile("branchInflationBonus($0, $1)", 2)
		lib.BranchInflationBonusPrecompiled.Store(expr)
	}
	var slotBin [4]byte
	var res []byte

	binary.BigEndian.PutUint32(slotBin[:], slot)
	err := util.CatchPanicOrError(func() error {
		res = easyfl.EvalExpressionWithSlicePool(nil, nil, expr, proof, slotBin[:])
		return nil
	})
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}
