package ledger

import (
	"fmt"

	_ "embed"

	"github.com/lunfardo314/proxima/util"
)

//go:embed def/sequencer.easyfl
var sequencerConstraintSource string

const (
	SequencerConstraintName = "sequencer"
)

// SequencerConstraint is a marker constraint with no parameters.
// The sibling chain constraint is always at index 2.
type SequencerConstraint struct{}

func NewSequencerConstraint() *SequencerConstraint {
	return &SequencerConstraint{}
}

func (s *SequencerConstraint) Name() string {
	return SequencerConstraintName
}

func (s *SequencerConstraint) Bytes() []byte {
	return mustBinFromSource(s.Source())
}

func (s *SequencerConstraint) String() string {
	return SequencerConstraintName
}

func (s *SequencerConstraint) Source() string {
	return SequencerConstraintName
}

// SequencerConstraintFromBytesWithLib parses a SequencerConstraint using the library
func SequencerConstraintFromBytesWithLib(data []byte, lib *Library) (*SequencerConstraint, error) {
	sym, _, _, err := lib.ParseBytecodeOneLevel(data, 0)
	if err != nil {
		return nil, err
	}
	if sym != SequencerConstraintName {
		return nil, fmt.Errorf("not a sequencer constraint")
	}
	return &SequencerConstraint{}, nil
}

func registerSequencerConstraint(lib *Library) {
	lib.mustRegisterConstraint(SequencerConstraintName, 0, func(data []byte) (Constraint, error) {
		return SequencerConstraintFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		example := NewSequencerConstraint()
		sym, _, _, err := lib.ParseBytecodeOneLevel(example.Bytes(), 0)
		util.AssertNoError(err)
		util.Assertf(sym == SequencerConstraintName, "sym == SequencerConstraintName")
	})
}
