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
	// sequencerTemplate emits the 3-arg form: epochSlots (z32),
	// maxFrozenEpochs (byte), coverageDelta (z64). epochSlots/maxFrozenEpochs
	// are the immutable delegation params; coverageDelta is the per-milestone
	// ledger coverage delta (mutable across transit, constrained strictly
	// increasing within a slot — see def/sequencer.easyfl).
	sequencerTemplate = SequencerConstraintName + "(z32/%d, %d, z64/%d)"

	// SequencerConstraintFixedIndex is the conventional position of
	// the sequencer constraint inside a sequencer chain output's
	// tuple. Established by sequencer-side compose and pinned by
	// the constraint body (selfBlockIndex == sequencerConstraintIndex).
	SequencerConstraintFixedIndex = 4
)

// SequencerConstraint marks a chain output as a sequencer chain. It carries the
// immutable delegation parameters (EpochSlots, MaxFrozenEpochs) plus the
// per-milestone CoverageDelta. See def/sequencer.easyfl.
type SequencerConstraint struct {
	EpochSlots      uint32
	MaxFrozenEpochs byte
	CoverageDelta   uint64
}

func NewSequencerConstraint(epochSlots uint32, maxFrozenEpochs byte, coverageDelta uint64) *SequencerConstraint {
	return &SequencerConstraint{
		EpochSlots:      epochSlots,
		MaxFrozenEpochs: maxFrozenEpochs,
		CoverageDelta:   coverageDelta,
	}
}

func (s *SequencerConstraint) Name() string {
	return SequencerConstraintName
}

func (s *SequencerConstraint) Bytes() []byte {
	return mustBinFromSource(s.Source())
}

func (s *SequencerConstraint) String() string {
	return fmt.Sprintf("%s(epochSlots=%d, maxFrozenEpochs=%d, coverageDelta=%d)",
		SequencerConstraintName, s.EpochSlots, s.MaxFrozenEpochs, s.CoverageDelta)
}

func (s *SequencerConstraint) Source() string {
	return fmt.Sprintf(sequencerTemplate, s.EpochSlots, s.MaxFrozenEpochs, s.CoverageDelta)
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
	if len(args) != 3 {
		return nil, fmt.Errorf("sequencer constraint: expected 3 args, got %d", len(args))
	}
	e0, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil || e0 > math.MaxUint32 {
		return nil, fmt.Errorf("SequencerConstraint: epochSlots out of range: %v", err)
	}
	e1, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil || e1 >= 256 {
		return nil, fmt.Errorf("SequencerConstraint: maxFrozenEpochs out of range: %v", err)
	}
	cd, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return nil, fmt.Errorf("SequencerConstraint: coverageDelta: %w", err)
	}
	return &SequencerConstraint{
		EpochSlots:      uint32(e0),
		MaxFrozenEpochs: byte(e1),
		CoverageDelta:   cd,
	}, nil
}

func registerSequencerConstraint(lib *Library) {
	lib.mustRegisterConstraint(SequencerConstraintName, 3, func(data []byte) (Constraint, error) {
		return SequencerConstraintFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		// Round-trip the default (600, 20) and the bounds corners
		// (500, 8) and (2000, 32), each with a sample coverageDelta.
		for _, ex := range []*SequencerConstraint{
			NewSequencerConstraint(600, 20, 1_000_000),
			NewSequencerConstraint(500, 8, 0),
			NewSequencerConstraint(2000, 32, math.MaxUint64),
		} {
			sym, _, _, err := lib.ParseBytecodeOneLevel(ex.Bytes(), 3)
			util.AssertNoError(err)
			util.Assertf(sym == SequencerConstraintName, "sym == SequencerConstraintName")
			back, err := SequencerConstraintFromBytesWithLib(ex.Bytes(), lib)
			util.AssertNoError(err)
			util.Assertf(back.EpochSlots == ex.EpochSlots, "epochSlots round-trip (%d)", ex.EpochSlots)
			util.Assertf(back.MaxFrozenEpochs == ex.MaxFrozenEpochs, "maxFrozenEpochs round-trip (%d)", ex.MaxFrozenEpochs)
			util.Assertf(back.CoverageDelta == ex.CoverageDelta, "coverageDelta round-trip (%d)", ex.CoverageDelta)
		}
	})
}
