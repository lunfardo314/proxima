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
	return uint64(forSlots) * (amount / (Const.MinimumInflatableAmount0 + uint64(inSlot)))
}

func ChainInflationOneSlot(amount uint64, inSlot uint32) uint64 {
	return ChainInflation(amount, inSlot, 1)
}

func (lib *Library) BranchInflationBonusBaseFromSource() uint64 {
	res, err := lib.EvalFromSource(nil, "constBranchInflationBonusBase")
	util.AssertNoError(err)
	return easyfl_util.MustUint64FromBytes(res)
}

func BranchInflationBonus(proof []byte) uint64 {
	return RandomFromSeed(proof, Const.BranchInflationBonusBase) + 1
}

func MinimumInflatableAmount(slot uint32) uint64 {
	return Const.MinimumInflatableAmount0 + ChainInflation(Const.MinimumInflatableAmount0, 0, slot)
}
