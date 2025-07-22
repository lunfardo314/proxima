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
