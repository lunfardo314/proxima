package ledger

import (
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

// Immutable constraint forces the specified DataBlock to be repeated on the successor of the specified chain

type Immutable struct {
	ChainBlockIndex byte
	DataBlockIndex  byte
}

const (
	ImmutableName     = "immutable"
	immutableTemplate = ImmutableName + "(0x%s)"
)

func NewImmutable(chainBlockIndex, dataBlockIndex byte) *Immutable {
	return &Immutable{
		ChainBlockIndex: chainBlockIndex,
		DataBlockIndex:  dataBlockIndex,
	}
}

func ImmutableFromBytes(data []byte) (*Immutable, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != ImmutableName {
		return nil, fmt.Errorf("not a Immutable")
	}
	d := easyfl.StripDataPrefix(args[0])
	if len(d) != 2 {
		return nil, fmt.Errorf("can't parse Immutable")
	}
	return NewImmutable(d[0], d[1]), nil
}

func (d *Immutable) Source() string {
	return fmt.Sprintf(immutableTemplate, hex.EncodeToString([]byte{d.ChainBlockIndex, d.DataBlockIndex}))
}

func (d *Immutable) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *Immutable) Name() string {
	return ImmutableName
}

func (d *Immutable) String() string {
	return d.Source()
}

func addImmutableConstraint(lib *Library) {
	lib.extendWithConstraint(ImmutableName, immutableDataSource, 1, func(data []byte) (Constraint, error) {
		return ImmutableFromBytes(data)
	}, initTestImmutableConstraint)
}

func initTestImmutableConstraint() {
	example := NewImmutable(1, 5)
	immutableDataBack, err := ImmutableFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(immutableDataBack.DataBlockIndex == 5, "inconsistency "+ImmutableName)
	util.Assertf(immutableDataBack.ChainBlockIndex == 1, "inconsistency "+ImmutableName)

	_, err = L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)
}

const immutableDataSource = `

// constraint 'immutable(c)' makes the sibling constraint immutable in the chain
// It requires unlock parameters 2-byte long:
// byte 0 points to the block of the chain successor in 'produced' side
// byte 1 point to the successor of the 'immutable' constraint itself
// The block must be exactly equal to the data block in the predecessor

// $0 - 2-byte array. [0] is chain constraint index, [1] - data block index
func immutable : or(
	and(
		selfIsProducedOutput,  // produced side
		equal(
			// 1st byte must point to the sibling-chain constraint
			parsePrefixBytecode(selfSiblingConstraint(byte($0,0))), 
			#chain
		), 
		selfSiblingConstraint(byte($0,1)) // 2nd byte must point to existing non-empty block
	),
	and(
		selfIsConsumedOutput,  // consumed side
		// we do not need to check correctness of the referenced chain constraint because it was 
		// already checked on the 'produced side
		equal(
			// referenced sibling constraint must repeat the data-constraint referenced in the unlock parameters
			// on the successor side. This is exact definition of immutability
			selfSiblingConstraint(byte($0,1)),  
			producedConstraintByIndex(
				concat(
					byte(selfSiblingUnlockBlock(byte($0,0)),0), // successor output index
					byte(selfUnlockParameters, 0)       // successor immutable data index
				)
			)
		),
		equal(
			// the 'immutable' constraint must repeat itself on the successor side too
			evalArgumentBytecode(
				producedConstraintByIndex(
					concat(
						byte(selfSiblingUnlockBlock(byte($0,0)),0), // successor output index
						byte(selfUnlockParameters, 1)               // successor 'immutable' constraint index
					)
				),
				selfBytecodePrefix,
				0
			),
			concat(
				byte(selfSiblingUnlockBlock(byte($0,0)),1),  // chain successor block index
				byte(selfUnlockParameters, 0)                // reference to the immutable data block
			)
		)
	),
	!!!immutable_constraint_failed
)
`
