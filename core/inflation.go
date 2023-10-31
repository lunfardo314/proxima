package core

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

type InflationConstraint struct {
	Amount                              uint64
	PredecessorSequencerConstraintIndex byte
}

const (
	InflationConstraintName     = "inflation"
	inflationConstraintTemplate = InflationConstraintName + "(u64/%d,%d)"
)

func NewInflationConstraint(amount uint64, predecessorSeqConstraintIndex byte) *InflationConstraint {
	return &InflationConstraint{
		Amount:                              amount,
		PredecessorSequencerConstraintIndex: predecessorSeqConstraintIndex,
	}
}

func InflationConstraintFromBytes(data []byte) (*InflationConstraint, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != InflationConstraintName {
		return nil, fmt.Errorf("not an inflation constraint")
	}
	amountBin := easyfl.StripDataPrefix(args[0])
	if len(amountBin) != 8 {
		return nil, fmt.Errorf("wrong data length")
	}
	preSeqIdxBin := easyfl.StripDataPrefix(args[1])
	if len(preSeqIdxBin) != 1 {
		return nil, fmt.Errorf("wrong data length")
	}
	return &InflationConstraint{
		Amount:                              binary.BigEndian.Uint64(amountBin),
		PredecessorSequencerConstraintIndex: preSeqIdxBin[0],
	}, nil
}

func (inf *InflationConstraint) source() string {
	return fmt.Sprintf(inflationConstraintTemplate, inf.Amount, inf.PredecessorSequencerConstraintIndex)
}

func (inf *InflationConstraint) Bytes() []byte {
	return mustBinFromSource(inf.source())
}

func (inf *InflationConstraint) Name() string {
	return InflationConstraintName
}

func (inf *InflationConstraint) String() string {
	return inf.source()
}

func initInflationConstraint() {
	easyfl.MustExtendMany(InflationLockConstraintSource)

	example := NewInflationConstraint(1337, 3)
	inflationLockBack, err := InflationConstraintFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(inflationLockBack, example), "inconsistency "+InflationConstraintName)

	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 2)
	util.AssertNoError(err)
	util.Assertf(sym == InflationConstraintName, "sym == InflationConstraintName")

	amountBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(len(amountBin) == 8, "len(amountBin) == 8")
	util.Assertf(binary.BigEndian.Uint64(amountBin) == 1337, "binary.BigEndian.Uint64(amountBin)==1337")

	predSeqIdxBin := easyfl.StripDataPrefix(args[1])
	util.Assertf(len(predSeqIdxBin) == 1, "len(predSeqIdxBin) == 1")
	util.Assertf(predSeqIdxBin[0] == 3, "predSeqIdxBin[0] == 3")

	registerConstraint(InflationConstraintName, prefix, func(data []byte) (Constraint, error) {
		return InflationConstraintFromBytes(data)
	})
}

// TODO not finished

const InflationLockConstraintSource = `
// $0 - ED25519 address, 32 byte blake2b hash of the public key
func inflation: or(
	selfIsConsumedOutput,
	require(isBranchTransaction, !!!inflation_can-only_be_on_branch_transaction),
    $0, $1
)
`
