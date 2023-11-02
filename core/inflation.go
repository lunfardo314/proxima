package core

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

type InflationConstraint struct {
	Amount                              uint64
	SequencerConstraintIndex            byte
	PredecessorSequencerConstraintIndex byte
}

const (
	InflationConstraintName     = "inflation"
	inflationConstraintTemplate = InflationConstraintName + "(u64/%d,%d,%d)"
)

func NewInflationConstraint(amount uint64, seqConstraintIndex, predecessorSeqConstraintIndex byte) *InflationConstraint {
	return &InflationConstraint{
		Amount:                              amount,
		SequencerConstraintIndex:            seqConstraintIndex,
		PredecessorSequencerConstraintIndex: predecessorSeqConstraintIndex,
	}
}

func InflationConstraintFromBytes(data []byte) (*InflationConstraint, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 3)
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
	seqIdxBin := easyfl.StripDataPrefix(args[1])
	if len(seqIdxBin) != 1 {
		return nil, fmt.Errorf("wrong data length")
	}
	preSeqIdxBin := easyfl.StripDataPrefix(args[2])
	if len(preSeqIdxBin) != 1 {
		return nil, fmt.Errorf("wrong data length")
	}
	return &InflationConstraint{
		Amount:                              binary.BigEndian.Uint64(amountBin),
		SequencerConstraintIndex:            seqIdxBin[0],
		PredecessorSequencerConstraintIndex: preSeqIdxBin[0],
	}, nil
}

func (inf *InflationConstraint) source() string {
	return fmt.Sprintf(inflationConstraintTemplate, inf.Amount, inf.SequencerConstraintIndex, inf.PredecessorSequencerConstraintIndex)
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

	example := NewInflationConstraint(1337, 4, 3)
	inflationLockBack, err := InflationConstraintFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(inflationLockBack, example), "inconsistency "+InflationConstraintName)

	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 3)
	util.AssertNoError(err)
	util.Assertf(sym == InflationConstraintName, "sym == InflationConstraintName")

	amountBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(len(amountBin) == 8, "len(amountBin) == 8")
	util.Assertf(binary.BigEndian.Uint64(amountBin) == 1337, "binary.BigEndian.Uint64(amountBin)==1337")

	seqIdxBin := easyfl.StripDataPrefix(args[1])
	util.Assertf(len(seqIdxBin) == 1, "len(predSeqIdxBin) == 1")
	util.Assertf(seqIdxBin[0] == 4, "predSeqIdxBin[0] == 4")

	predSeqIdxBin := easyfl.StripDataPrefix(args[2])
	util.Assertf(len(predSeqIdxBin) == 1, "len(predSeqIdxBin) == 1")
	util.Assertf(predSeqIdxBin[0] == 3, "predSeqIdxBin[0] == 3")

	registerConstraint(InflationConstraintName, prefix, func(data []byte) (Constraint, error) {
		return InflationConstraintFromBytes(data)
	})
}

// TODO not finished

const InflationLockConstraintSource = `

// $0 - sequencer constraint index on produced output
// Returns - chain predecessor (consumed) output 
// by following 'sequencer' -> 'chain' -> chain predecessor index -> chain predecessor
func sequencerPredecessorOutput: 
	consumedOutputByIndex(
		 // take chain predecessor index frm chain data 
		 predecessorInputIndexFromChainData(
			  // 'chain' constraint, 0 arg -> chain data  
			  unwrapBytecodeArg(
				   selfSiblingConstraint(
						// 'sequencer' constraint, arg 0 -> sibling chain constraint index (on same self output)
						unwrapBytecodeArg(
							 selfSiblingConstraint($0), 
							 #sequencer, 
							 0
						)
				   ), 
				   #chain, 
				   0
			  )
		 )
	)

// $0 - sequencer constraint index on the current (produced) output
// $1 - sequencer constraint index on the chain predecessor output (consumed)
func predecessorAmount: unwrapBytecodeArg(
    @Array8( sequencerPredecessorOutput($0), $1 ), 
    #sequencer, 
    1
)

// $0 - inflation amount
// $1 - sequencer constraint index on the current (produced) output
// $2 - sequencer constraint index on the chain predecessor output (consumed)
func inflation: if(
	selfIsProducedOutput,
    and(
		require(isBranchTransaction, !!!inflation_can_only_be_on_branch_transaction),
		require(equal(txTotalProducedAmountBytes, predecessorAmount($1, $2)), !!!not_equal_amount)
    ),
    true
)
`
