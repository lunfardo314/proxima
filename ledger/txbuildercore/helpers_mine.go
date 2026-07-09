package txbuildercore

import (
	"fmt"
	"math"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
)

// MineLockName is the symbol of the fair-launch mine chain lock.
// Matches ledger/lock_mine.go.
const MineLockName = "mineLock"

// mineLockTemplate mirrors ledger.MineLockTemplate: args are
// (R, B, s3, s2, s1) — the slot ring passed oldest-to-newest.
const mineLockTemplate = MineLockName + "(z64/%d, z64/%d, z32/%d, z32/%d, z32/%d)"

// MineLockView is the wallet-side decoded mineLock at output element
// index 2 of the single mine chain UTXO. Mirrors ledger.MineLock
// field-for-field.
//
//	R  remaining mintable motes (decreases by A each transit)
//	B  current base/max difficulty in bits
//	S1 newest ring slot, S3 oldest
type MineLockView struct {
	R  uint64
	B  uint64
	S1 uint32
	S2 uint32
	S3 uint32
}

// NewMineLock emits the 5-arg mineLock bytecode. Byte-identical to
// ledger.NewMineLock(r, b, s1, s2, s3).Bytes().
func (l *Library[any]) NewMineLock(r, b uint64, s1, s2, s3 uint32) ([]byte, error) {
	return l.CompileExpression(fmt.Sprintf(mineLockTemplate, r, b, s3, s2, s1))
}

// ParseMineLock decodes mineLock bytecode. Pure byte parse — no eval.
// Mirrors ledger.MineLockFromBytesWithLib.
func (l *Library[any]) ParseMineLock(data []byte) (*MineLockView, error) {
	sym, _, args, err := l.ParseBytecodeOneLevel(data, 5)
	if err != nil {
		return nil, fmt.Errorf("ParseMineLock: %w", err)
	}
	if sym != MineLockName {
		return nil, fmt.Errorf("ParseMineLock: expected %s, got %s", MineLockName, sym)
	}
	ret := &MineLockView{}
	if ret.R, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0])); err != nil {
		return nil, fmt.Errorf("ParseMineLock: R: %w", err)
	}
	if ret.B, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1])); err != nil {
		return nil, fmt.Errorf("ParseMineLock: B: %w", err)
	}
	// args are ring oldest-to-newest: s3, s2, s1.
	s3, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil || s3 > math.MaxUint32 {
		return nil, fmt.Errorf("ParseMineLock: s3: %w", err)
	}
	s2, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil || s2 > math.MaxUint32 {
		return nil, fmt.Errorf("ParseMineLock: s2: %w", err)
	}
	s1, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[4]))
	if err != nil || s1 > math.MaxUint32 {
		return nil, fmt.Errorf("ParseMineLock: s1: %w", err)
	}
	ret.S3, ret.S2, ret.S1 = uint32(s3), uint32(s2), uint32(s1)
	return ret, nil
}
