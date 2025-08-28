package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// CalcChainInflationAmountOneSlot calculates inflation for one slot. Inflation cannot be bigger than one slot.
// This makes token holder move the output every slot to earn maximum inflation
func (lib *Library) CalcChainInflationAmountOneSlot(inSlot base.Slot, inCoverage uint64) uint64 {
	return inCoverage / (lib.ID.SlotInflationFraction + uint64(inSlot))
}

func (lib *Library) CalcChainInflationAmountOneSlotFromSource(inSlot base.Slot, inCoverage uint64) uint64 {
	src := fmt.Sprintf("chainInflationOneSlot(u64/%d, u64/%d)", inSlot, inCoverage)
	resBin, err := lib.EvalFromSource(nil, src)
	util.AssertNoError(err)
	return binary.BigEndian.Uint64(resBin)
}

func (lib *Library) BranchInflationBonusBase() uint64 {
	res, err := lib.EvalFromSource(nil, "constBranchInflationBonusBase")
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

func (lib *Library) BranchInflationBonusDirect(proof []byte) uint64 {
	return RandomFromSeed(proof, lib.BranchInflationBonusBase()) + 1
}

func InflationProjection(amount uint64, slots uint32) uint64 {
	total := amount
	for s := uint32(0); s < slots; s++ {
		total += L().CalcChainInflationAmountOneSlot(base.Slot(s), total)
	}
	return total - amount
}
