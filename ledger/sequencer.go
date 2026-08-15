package ledger

import (
	"fmt"
	"math"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
)

//go:embed def/sequencer.easyfl
var sequencerConstraintSource string

const (
	SequencerConstraintName = "sequencer"
	// sequencerTemplate emits the 1-arg form: coverageDelta (z64), the
	// per-milestone ledger coverage delta. Mutable across transit and
	// constrained strictly increasing within a slot — see def/sequencer.easyfl.
	sequencerTemplate = SequencerConstraintName + "(z64/%d)"

	// SequencerConstraintFixedIndex is the conventional position of
	// the sequencer constraint inside a sequencer chain output's
	// tuple. Established by sequencer-side compose and pinned by
	// the constraint body (selfBlockIndex == sequencerConstraintIndex).
	SequencerConstraintFixedIndex = 4
)

// SequencerConstraint marks a chain output as a sequencer chain and carries the
// per-milestone CoverageDelta. The delegation parameters it used to carry are
// ledger constants now. See def/sequencer.easyfl.
type SequencerConstraint struct {
	CoverageDelta uint64
}

func NewSequencerConstraint(coverageDelta uint64) *SequencerConstraint {
	return &SequencerConstraint{CoverageDelta: coverageDelta}
}

func (s *SequencerConstraint) Name() string {
	return SequencerConstraintName
}

func (s *SequencerConstraint) Bytes() []byte {
	return mustBinFromSource(s.Source())
}

func (s *SequencerConstraint) String() string {
	return fmt.Sprintf("%s(coverageDelta=%d)", SequencerConstraintName, s.CoverageDelta)
}

func (s *SequencerConstraint) Source() string {
	return fmt.Sprintf(sequencerTemplate, s.CoverageDelta)
}

// SequencerConstraintFromBytesWithLib parses a SequencerConstraint using the library.
// Returns ("not a sequencer constraint") error when data is non-empty bytecode of a
// different symbol — callers that probe "is this slot a sequencer constraint?" use
// this signal instead of panicking on arity mismatch.
func SequencerConstraintFromBytesWithLib(data []byte, lib *Library) (*SequencerConstraint, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data)
	if err != nil {
		return nil, err
	}
	if sym != SequencerConstraintName {
		return nil, fmt.Errorf("not a sequencer constraint")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("sequencer constraint: expected 1 arg, got %d", len(args))
	}
	cd, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, fmt.Errorf("SequencerConstraint: coverageDelta: %w", err)
	}
	return &SequencerConstraint{CoverageDelta: cd}, nil
}

func registerSequencerConstraint(lib *Library) {
	lib.mustRegisterConstraint(SequencerConstraintName, 1, func(data []byte) (Constraint, error) {
		return SequencerConstraintFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		// Round-trip the coverageDelta corners.
		for _, ex := range []*SequencerConstraint{
			NewSequencerConstraint(1_000_000),
			NewSequencerConstraint(0),
			NewSequencerConstraint(math.MaxUint64),
		} {
			sym, _, _, err := lib.ParseBytecodeOneLevel(ex.Bytes(), 1)
			util.AssertNoError(err)
			util.Assertf(sym == SequencerConstraintName, "sym == SequencerConstraintName")
			back, err := SequencerConstraintFromBytesWithLib(ex.Bytes(), lib)
			util.AssertNoError(err)
			util.Assertf(back.CoverageDelta == ex.CoverageDelta, "coverageDelta round-trip (%d)", ex.CoverageDelta)
		}
	})
}
