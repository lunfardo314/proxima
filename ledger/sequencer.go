package ledger

import (
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

//go:embed def/sequencer.efl
var sequencerConstraintSource string

const (
	SequencerConstraintName     = "sequencer"
	sequencerConstraintTemplate = SequencerConstraintName + "(%d)"
)

type (
	SequencerConstraint struct {
		// must point to the sibling chain constraint
		ChainConstraintIndex byte
	}
	// SequencerTransactionData represents sequencer and stem data on the transaction
	SequencerTransactionData struct {
		SequencerOutputData  *SequencerOutputData
		StemOutputData       *StemLock    // nil if does not contain stem output
		SequencerID          base.ChainID // adjusted for chain origin
		SequencerOutputIndex byte
		StemOutputIndex      byte // 0xff if not a branch transaction
		DepthBudget          byte
	}
)

func (m *SequencerTransactionData) Short() string {
	return fmt.Sprintf("SEQ(%s)", m.SequencerID.StringVeryShort())
}

func (m *SequencerTransactionData) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("seq output index: %d, branch output index: %d, depth budget: %d", m.SequencerOutputIndex, m.SequencerOutputIndex, m.DepthBudget)
	ret.Add("seq ID: %s", m.SequencerID.String())
	return ret
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
