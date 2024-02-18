package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

// MilestoneData data which is on sequencer as 'or(..)' constraint. It is not enforced by the ledger, yet maintained
// by the sequencer
type MilestoneData struct {
	Name         string // < 256
	MinimumFee   uint64
	ChainHeight  uint32
	BranchHeight uint32
}

const MilestoneDataFixedIndex = 4

// ParseMilestoneData expected at index 4, otherwise nil
func ParseMilestoneData(o *Output) *MilestoneData {
	if o.NumConstraints() <= MilestoneDataFixedIndex {
		return nil
	}
	ret, err := MilestoneDataFromConstraint(o.ConstraintAt(MilestoneDataFixedIndex))
	if err != nil {
		return nil
	}
	return ret
}

func (od *MilestoneData) AsConstraint() Constraint {
	dscrBin := []byte(od.Name)
	if len(dscrBin) > 255 {
		dscrBin = dscrBin[:256]
	}
	dscrBinStr := fmt.Sprintf("0x%s", hex.EncodeToString(dscrBin))
	chainIndexStr := fmt.Sprintf("u32/%d", od.ChainHeight)
	branchIndexStr := fmt.Sprintf("u32/%d", od.BranchHeight)
	minFeeStr := fmt.Sprintf("u64/%d", od.MinimumFee)

	src := fmt.Sprintf("or(%s)", strings.Join([]string{dscrBinStr, chainIndexStr, branchIndexStr, minFeeStr}, ","))
	_, _, bytecode, err := L().CompileExpression(src)
	util.AssertNoError(err)

	constr, err := ConstraintFromBytes(bytecode)
	util.AssertNoError(err)

	return constr
}

func MilestoneDataFromConstraint(constr []byte) (*MilestoneData, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(constr)
	if err != nil {
		return nil, err
	}
	if sym != "or" {
		return nil, fmt.Errorf("sequencer.MilestoneDataFromConstraint: unexpected function '%s'", sym)
	}
	if len(args) != 4 {
		return nil, fmt.Errorf("sequencer.MilestoneDataFromConstraint: expected exactly 4 arguments, got %d", len(args))
	}
	dscrBin := easyfl.StripDataPrefix(args[0])
	chainIdxBin := easyfl.StripDataPrefix(args[1])
	branchIdxBin := easyfl.StripDataPrefix(args[2])
	minFeeBin := easyfl.StripDataPrefix(args[3])
	if len(chainIdxBin) != 4 || len(branchIdxBin) != 4 || len(minFeeBin) != 8 {
		return nil, fmt.Errorf("sequencer.MilestoneDataFromConstraint: unexpected argument sizes %d, %d, %d, %d",
			len(args[0]), len(args[1]), len(args[2]), len(args[3]))
	}
	return &MilestoneData{
		Name:         string(dscrBin),
		ChainHeight:  binary.BigEndian.Uint32(chainIdxBin),
		BranchHeight: binary.BigEndian.Uint32(branchIdxBin),
		MinimumFee:   binary.BigEndian.Uint64(minFeeBin),
	}, nil
}
