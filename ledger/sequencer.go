package ledger

import (
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

//go:embed def/sequencer.efl
var sequencerConstraintSource string

const (
	SequencerConstraintName     = "sequencer"
	sequencerConstraintTemplate = SequencerConstraintName + "(%d)"
)

type SequencerConstraint struct {
	// must point to the sibling chain constraint
	ChainConstraintIndex byte
}

func NewSequencerConstraint(chainConstraintIndex byte) *SequencerConstraint {
	return &SequencerConstraint{
		ChainConstraintIndex: chainConstraintIndex,
	}
}

func (s *SequencerConstraint) Name() string {
	return SequencerConstraintName
}

func (s *SequencerConstraint) Bytes() []byte {
	return mustBinFromSource(s.Source())
}

func (s *SequencerConstraint) String() string {
	return fmt.Sprintf("%s(%d)", SequencerConstraintName, s.ChainConstraintIndex)
}

func (s *SequencerConstraint) Source() string {
	return fmt.Sprintf(sequencerConstraintTemplate, s.ChainConstraintIndex)
}

// SequencerConstraintFromBytesWithLib parses a SequencerConstraint using the library
func SequencerConstraintFromBytesWithLib(data []byte, lib *Library) (*SequencerConstraint, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != SequencerConstraintName {
		return nil, fmt.Errorf("not a sequencerConstraintIndex")
	}
	cciBin := easyfl.StripDataPrefix(args[0])
	if len(cciBin) != 1 {
		return nil, fmt.Errorf("wrong chainConstraintIndex parameter")
	}
	cci := cciBin[0]

	return &SequencerConstraint{
		ChainConstraintIndex: cci,
	}, nil
}

func registerSequencerConstraint(lib *Library) {
	lib.mustRegisterConstraint(SequencerConstraintName, 1, func(data []byte) (Constraint, error) {
		return SequencerConstraintFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		example := NewSequencerConstraint(4)
		sym, _, args, err := lib.ParseBytecodeOneLevel(example.Bytes(), 1)
		util.AssertNoError(err)
		util.Assertf(sym == SequencerConstraintName, "sym == SequencerConstraintName")

		cciBin := easyfl.StripDataPrefix(args[0])
		util.Assertf(len(cciBin) == 1, "len(cciBin) == 1")
		util.Assertf(cciBin[0] == 4, "cciBin[0] == 4")
	})
}
