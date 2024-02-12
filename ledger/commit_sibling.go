package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// the CommitToSibling constraint ensures that $1 is equal to the blake2b hash of the produced output,
// referenced by $0
// This is the way to ensure trust-less Proof Of Possession in the same transaction. When provided
// some other output can be checked if it was committed by this constraint
// NOTE: cross-commit between two produced outputs is not possible

type CommitToSibling struct {
	SiblingIndex byte
	SiblingHash  []byte
}

const (
	CommitToSiblingName     = "commitToSibling"
	commitToSiblingTemplate = CommitToSiblingName + "(%d, 0x%s)"
)

func NewCommitToSibling(siblingIndex byte, siblingHash []byte) *CommitToSibling {
	return &CommitToSibling{
		SiblingIndex: siblingIndex,
		SiblingHash:  siblingHash,
	}
}

func CommitToSiblingFromBytes(data []byte) (*CommitToSibling, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != CommitToSiblingName {
		return nil, fmt.Errorf("not a CommitToSibling")
	}
	idxBin := easyfl.StripDataPrefix(args[0])
	if len(idxBin) != 1 {
		return nil, fmt.Errorf("wrong sibling output index")
	}
	hash := easyfl.StripDataPrefix(args[1])
	if len(hash) != 32 {
		return nil, fmt.Errorf("wrong sibling hash")
	}
	return NewCommitToSibling(idxBin[0], hash), nil
}

func (cs *CommitToSibling) source() string {
	return fmt.Sprintf(commitToSiblingTemplate, cs.SiblingIndex, hex.EncodeToString(cs.SiblingHash))
}

func (cs *CommitToSibling) Bytes() []byte {
	return mustBinFromSource(cs.source())
}

func (cs *CommitToSibling) Name() string {
	return CommitToSiblingName
}

func (cs *CommitToSibling) String() string {
	return cs.source()
}

func addCommitToSiblingConstraint(lib *Library) {
	lib.extendWithConstraint(CommitToSiblingName, commitToSiblingSource, 2, func(data []byte) (Constraint, error) {
		return CommitToSiblingFromBytes(data)
	}, initTestCommitToSiblingConstraint)
}

func initTestCommitToSiblingConstraint() {
	h := blake2b.Sum256([]byte("just data"))
	example := NewCommitToSibling(2, h[:])
	csBack, err := CommitToSiblingFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(csBack.SiblingIndex == 2, "inconsistency "+CommitToSiblingName)
	util.Assertf(bytes.Equal(csBack.SiblingHash, h[:]), "inconsistency "+CommitToSiblingName)

	_, err = L().ParseBytecodePrefix(example.Bytes())
	util.AssertNoError(err)
}

const commitToSiblingSource = `
func commitToSibling : or(
	and(
		selfIsConsumedOutput,
			// should not commit to itself
		not(equal($0, selfOutputIndex))
	),
	and(
		selfIsProducedOutput,
			// hash of the referenced produced output must be equal to the argument
		equal(
			$1,
			blake2b(producedOutputByIndex($0))
		)
	),
	!!!commitToSibling_constraint_failed
)
`
