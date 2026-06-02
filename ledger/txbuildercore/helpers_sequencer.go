package txbuildercore

import (
	"fmt"
	"math"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
)

// SequencerConstraintName is the canonical symbol for the 2-arg
// sequencer constraint that marks a chain output as a sequencer chain
// and carries its immutable delegation params. Mirrors
// ledger.SequencerConstraintName.
const SequencerConstraintName = "sequencer"

// SequencerConstraintFixedIndex is the conventional position of the
// sequencer constraint inside a sequencer chain output's tuple.
// Mirrors ledger.SequencerConstraintFixedIndex.
const SequencerConstraintFixedIndex = 4

const sequencerConstraintTemplate = SequencerConstraintName + "(z32/%d, %d)"

// SequencerConstraintView is the wallet-side decoded form of the
// 2-arg `sequencer(epochSlots, maxFrozenEpochs)` constraint. Mirrors
// ledger.SequencerConstraint field-for-field.
type SequencerConstraintView struct {
	EpochSlots      uint32
	MaxFrozenEpochs byte
}

// NewSequencerConstraintBytecode emits the bytecode of the 2-arg
// sequencer constraint. Singleton-free byte compile via the wallet
// library.
func (l *Library[any]) NewSequencerConstraintBytecode(epochSlots uint32, maxFrozenEpochs byte) ([]byte, error) {
	src := fmt.Sprintf(sequencerConstraintTemplate, epochSlots, maxFrozenEpochs)
	bin, err := l.CompileExpression(src)
	if err != nil {
		return nil, fmt.Errorf("NewSequencerConstraintBytecode: %w", err)
	}
	return bin, nil
}

// ParseSequencerConstraint decodes a sequencer constraint bytecode.
// Pure byte parse — no eval. Mirrors
// ledger.SequencerConstraintFromBytesWithLib.
func (l *Library[any]) ParseSequencerConstraint(data []byte) (*SequencerConstraintView, error) {
	sym, _, args, err := l.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, fmt.Errorf("ParseSequencerConstraint: %w", err)
	}
	if sym != SequencerConstraintName {
		return nil, fmt.Errorf("ParseSequencerConstraint: expected %s, got %s", SequencerConstraintName, sym)
	}
	e0, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil || e0 > math.MaxUint32 {
		return nil, fmt.Errorf("ParseSequencerConstraint: epochSlots out of range: %v", err)
	}
	e1, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1]))
	if err != nil || e1 >= 256 {
		return nil, fmt.Errorf("ParseSequencerConstraint: maxFrozenEpochs out of range: %v", err)
	}
	return &SequencerConstraintView{
		EpochSlots:      uint32(e0),
		MaxFrozenEpochs: byte(e1),
	}, nil
}
