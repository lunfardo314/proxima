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
// transit at gap M = succSlot - predSlot must actually satisfy. Flat at B up to
// MineReliefPace; beyond it, one bit of relief per extra slot down to the floor.
// The relief valve guarantees the chain can never stay stuck when B overshoots
// the network hashrate — waiting long enough always drops K to a solvable level.
func (c *Constants) MineRequiredK(b uint64, gap uint64) uint64 {
	if gap <= c.MineReliefPace {
		return b
	}
	relief := gap - c.MineReliefPace
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
// against a real successor slot is meaningless. If the gap exceeds MineReliefPace
// the chain was stuck, so B snaps down to what was actually solvable
// (MineRequiredK) — one-transit recovery. Otherwise the gap is compared to the
// target pace: below target means mining is too fast (harden one bit), above
// means too slow (ease one bit), equal means hold. Clamped to [floor, ceiling].
func (c *Constants) MineAdjustedB(predB uint64, predSlot, succSlot uint32) uint64 {
	if predSlot == 0 || succSlot < predSlot {
		return predB
	}
	gap := uint64(succSlot - predSlot)
	switch {
	case gap > c.MineReliefPace:
		return c.MineRequiredK(predB, gap)
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
