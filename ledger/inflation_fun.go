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

func (lib *Library) ChainInflation(amount uint64, inSlot, forSlots uint32) uint64 {
	return uint64(forSlots) * (amount / (lib.ID.MinimumInflatableAmount0 + uint64(inSlot)))
}

func (lib *Library) ChainInflationOneSlot(amount uint64, inSlot uint32) uint64 {
	return lib.ChainInflation(amount, inSlot, 1)
}

func (lib *Library) BranchInflationBonusBase() uint64 {
	res, err := lib.EvalFromSource(nil, "constBranchInflationBonusBase")
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

func (lib *Library) BranchInflationBonusDirect(proof []byte) uint64 {
	return RandomFromSeed(proof, lib.BranchInflationBonusBase()) + 1
}

func (lib *Library) MinimumInflatableAmount(slot uint32) uint64 {
	return lib.ID.MinimumInflatableAmount0 + lib.ChainInflation(lib.ID.MinimumInflatableAmount0, 0, slot)
}
