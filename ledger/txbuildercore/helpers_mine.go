package txbuildercore

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
)

// MineLockName is the symbol of the fair-launch mine chain lock.
// Matches ledger/lock_mine.go.
const MineLockName = "mineLock"

// mineLockTemplate mirrors ledger.MineLockTemplate: args are (R, B).
const mineLockTemplate = MineLockName + "(z64/%d, z64/%d)"

// MineLockView is the wallet-side decoded mineLock at output element
// index 2 of the single mine chain UTXO. Mirrors ledger.MineLock
// field-for-field.
//
//	R  remaining mintable motes (decreases by A each transit)
//	B  current difficulty in bits
type MineLockView struct {
	R uint64
	B uint64
}

// NewMineLock emits the 2-arg mineLock bytecode. Byte-identical to
// ledger.NewMineLock(r, b).Bytes().
func (l *Library[any]) NewMineLock(r, b uint64) ([]byte, error) {
	return l.CompileExpression(fmt.Sprintf(mineLockTemplate, r, b))
}

// ParseMineLock decodes mineLock bytecode. Pure byte parse — no eval.
// Mirrors ledger.MineLockFromBytesWithLib.
func (l *Library[any]) ParseMineLock(data []byte) (*MineLockView, error) {
	sym, _, args, err := l.ParseBytecodeOneLevel(data, 2)
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
	return ret, nil
}

// MineRequiredK mirrors _mineRequiredK in def/lock_mine.easyfl: the difficulty a
// transit at gap M = succSlot - predSlot must satisfy, K = max(B - (M - P), E).
// At the minimum pace M = P it is the full B; each extra slot of pace eases one
// bit, floored at the floor difficulty E. The caller only mines transits with
// gap >= P (the pace floor the constraint enforces), matching the mirror. This
// pace-relieved K also doubles as the liveness valve: K falls to E as the gap
// grows, so however far B sits above the network hashrate a big enough gap is
// always solvable and the chain can never wedge on difficulty.
func (c *Constants) MineRequiredK(b uint64, gap uint64) uint64 {
	if gap <= c.MineMinPace {
		return b
	}
	relief := gap - c.MineMinPace
	if b <= c.MineFloorDifficulty+relief {
		return c.MineFloorDifficulty
	}
	return b - relief
}

// MineAdjustedB mirrors the mineLock retarget (_mineAdjustedB in
// def/lock_mine.easyfl): the difficulty the successor must carry, from the
// single last gap M = succSlot - predSlot.
//
// B is held while the predecessor is the genesis mine output (slot 0), whose gap
// against a real successor slot is meaningless. Otherwise the gap is compared to
// the target pace: below target means mining is too fast (harden one bit), above
// means too slow (ease one bit), equal means hold. Clamped to [floor, ceiling].
// The retarget stays ±1 (no snap-down): the pace-relieved K leaves no large
// overshoot to recover from, so a single bit of ease per transit is enough.
func (c *Constants) MineAdjustedB(predB uint64, predSlot, succSlot uint32) uint64 {
	if predSlot == 0 || succSlot < predSlot {
		return predB
	}
	gap := uint64(succSlot - predSlot)
	switch {
	case gap < c.MineTargetPace:
		if predB >= c.MineMaxDifficulty {
			return c.MineMaxDifficulty
		}
		return predB + 1
	case gap > c.MineTargetPace:
		if predB <= c.MineFloorDifficulty {
			return c.MineFloorDifficulty
		}
		return predB - 1
	}
	return predB
}
