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
	// sequencerTemplate emits the 2-arg form: epochSlots (z32),
	// maxFrozenEpochs (byte). The chain origin that carries this
	// constraint advertises the chain as a sequencer chain that
	// always accepts delegations with these immutable params.
	sequencerTemplate = SequencerConstraintName + "(z32/%d, %d)"

	// SequencerConstraintFixedIndex is the conventional position of
	// the sequencer constraint inside a sequencer chain output's
	// tuple. Established by sequencer-side compose and locked by
	// selfImmutableOnSuccessorIndex on the constraint body.
	SequencerConstraintFixedIndex = 4
)

// SequencerConstraint marks a chain output as a sequencer chain and
// carries its immutable delegation parameters. See def/sequencer.easyfl.
type SequencerConstraint struct {
	EpochSlots      uint32
	MaxFrozenEpochs byte
}

func NewSequencerConstraint(epochSlots uint32, maxFrozenEpochs byte) *SequencerConstraint {
	return &SequencerConstraint{
		EpochSlots:      epochSlots,
		MaxFrozenEpochs: maxFrozenEpochs,
	}
}

func (s *SequencerConstraint) Name() string {
	return SequencerConstraintName
}

func (s *SequencerConstraint) Bytes() []byte {
	return mustBinFromSource(s.Source())
}

func (s *SequencerConstraint) String() string {
	return fmt.Sprintf("%s(epochSlots=%d, maxFrozenEpochs=%d)",
		SequencerConstraintName, s.EpochSlots, s.MaxFrozenEpochs)
}

func (s *SequencerConstraint) Source() string {
	return fmt.Sprintf(sequencerTemplate, s.EpochSlots, s.MaxFrozenEpochs)
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
	if len(args) != 2 {
		return nil, fmt.Errorf("sequencer constraint: expected 2 args, got %d", len(args))
	}
	e0, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil || e0 > math.MaxUint32 {
		return nil, fmt.Errorf("SequencerConstraint: epochSlots out of range: %v", err)
	}
	e1, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil || e1 >= 256 {
		return nil, fmt.Errorf("SequencerConstraint: maxFrozenEpochs out of range: %v", err)
	}
	return &SequencerConstraint{
		EpochSlots:      uint32(e0),
		MaxFrozenEpochs: byte(e1),
	}, nil
}

func registerSequencerConstraint(lib *Library) {
	lib.mustRegisterConstraint(SequencerConstraintName, 2, func(data []byte) (Constraint, error) {
		return SequencerConstraintFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		// Round-trip the default (600, 20) and the bounds corners
		// (500, 8) and (2000, 32).
		for _, ex := range []*SequencerConstraint{
			NewSequencerConstraint(600, 20),
			NewSequencerConstraint(500, 8),
			NewSequencerConstraint(2000, 32),
		} {
			sym, _, _, err := lib.ParseBytecodeOneLevel(ex.Bytes(), 2)
			util.AssertNoError(err)
			util.Assertf(sym == SequencerConstraintName, "sym == SequencerConstraintName")
			back, err := SequencerConstraintFromBytesWithLib(ex.Bytes(), lib)
			util.AssertNoError(err)
			util.Assertf(back.EpochSlots == ex.EpochSlots, "epochSlots round-trip (%d)", ex.EpochSlots)
			util.Assertf(back.MaxFrozenEpochs == ex.MaxFrozenEpochs, "maxFrozenEpochs round-trip (%d)", ex.MaxFrozenEpochs)
		}
	})
}
