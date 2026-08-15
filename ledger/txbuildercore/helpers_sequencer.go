package txbuildercore

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
)

// SequencerConstraintName is the canonical symbol for the 3-arg
// sequencer constraint that marks a chain output as a sequencer chain,
// carries its immutable delegation params and the per-milestone
// coverageDelta. Mirrors ledger.SequencerConstraintName.
const SequencerConstraintName = "sequencer"

// SequencerConstraintFixedIndex is the conventional position of the
// sequencer constraint inside a sequencer chain output's tuple.
// Mirrors ledger.SequencerConstraintFixedIndex.
const SequencerConstraintFixedIndex = 4

const sequencerConstraintTemplate = SequencerConstraintName + "(z64/%d)"

// SequencerConstraintView is the wallet-side decoded form of the 1-arg
// `sequencer(coverageDelta)` constraint. Mirrors ledger.SequencerConstraint
// field-for-field. The delegation parameters it used to carry are ledger
// constants now, available from Constants.
type SequencerConstraintView struct {
	CoverageDelta uint64
}

// NewSequencerConstraintBytecode emits the bytecode of the 1-arg
// sequencer constraint. Singleton-free byte compile via the wallet
// library.
func (l *Library[any]) NewSequencerConstraintBytecode(coverageDelta uint64) ([]byte, error) {
	src := fmt.Sprintf(sequencerConstraintTemplate, coverageDelta)
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
	sym, _, args, err := l.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, fmt.Errorf("ParseSequencerConstraint: %w", err)
	}
	if sym != SequencerConstraintName {
		return nil, fmt.Errorf("ParseSequencerConstraint: expected %s, got %s", SequencerConstraintName, sym)
	}
	cd, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, fmt.Errorf("ParseSequencerConstraint: coverageDelta: %v", err)
	}
	return &SequencerConstraintView{CoverageDelta: cd}, nil
}
