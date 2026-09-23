package txbuildercore

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
)

// MineLockName is the symbol of the fair-launch mine chain lock.
// Matches ledger/lock_mine.go.
const MineLockName = "mineLock"

// mineLockTemplate mirrors ledger.MineLockTemplate: args are (R, B, C).
const mineLockTemplate = MineLockName + "(z64/%d, z64/%d, z64/%d)"

// MineSettlementWindowTicks is the width of the settlement window at the end
// of a slot in which sequencers pick the canonical winner among the slot's
// mine transits (kb/mine_conflict_rule.md), counted back from the pre-branch
// consolidation zone. A miner's round for a slot ends where the window
// begins: a solution found later reaches no sequencer in time.
const MineSettlementWindowTicks = 16

// MineSettlementTick is the first tick of the settlement window in a slot.
func (c *Constants) MineSettlementTick() byte {
	return byte(c.TicksPerSlot) - c.PreBranchConsolidationTicks - MineSettlementWindowTicks
}

// MineLockView is the wallet-side decoded mineLock at output element
// index 2 of the single mine chain UTXO. Mirrors ledger.MineLock
// field-for-field.
//
//	R  remaining mintable motes (decreases by A each transit)
//	B  current difficulty in bits
//	C  full slots in a row since the last harden
type MineLockView struct {
	R uint64
	B uint64
	C uint64
}

// NewMineLock emits the 3-arg mineLock bytecode. Byte-identical to
// ledger.NewMineLock(r, b, c).Bytes().
func (l *Library[any]) NewMineLock(r, b, c uint64) ([]byte, error) {
	return l.CompileExpression(fmt.Sprintf(mineLockTemplate, r, b, c))
}

// ParseMineLock decodes mineLock bytecode. Pure byte parse — no eval.
// Mirrors ledger.MineLockFromBytesWithLib.
func (l *Library[any]) ParseMineLock(data []byte) (*MineLockView, error) {
	sym, _, args, err := l.ParseBytecodeOneLevel(data, 3)
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
	if ret.C, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2])); err != nil {
		return nil, fmt.Errorf("ParseMineLock: C: %w", err)
	}
	return ret, nil
}

// MineAmountAtSlot mirrors _mineAmountAtSlot in def/lock_mine.easyfl: the amount
// A minted by a transit landing in the given slot. Flat at MineAmountBase up to
// and including MineRampStartSlot, then growing by MineAmountPerSlot per slot.
// Callers must pass the SUCCESSOR slot, since that is the transaction the
// constraint validates.
func (c *Constants) MineAmountAtSlot(slot uint32) uint64 {
	if slot <= c.MineRampStartSlot {
		return c.MineAmountBase
	}
	return c.MineAmountBase + uint64(slot-c.MineRampStartSlot)*c.MineAmountPerSlot
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

// MineRetarget mirrors the mineLock retarget (_mineAdjustedB and
// _mineAdjustedC in def/lock_mine.easyfl): the difficulty and the full-slot
// count the successor must carry, from the single last gap
// M = succSlot - predSlot.
//
// Both are held while the predecessor is the genesis mine output (slot 0),
// whose gap against a real successor slot is meaningless. A full slot (gap 1)
// counts one more; when the count reaches MineHardenAfter the difficulty
// hardens one bit, clamped at the ceiling, and the count restarts. Empty slots
// (gap 2 or more) ease one bit each, floored, and leave the count as it is.
func (c *Constants) MineRetarget(predB, predC uint64, predSlot, succSlot uint32) (b, count uint64) {
	// a same-slot successor is below the minimum pace and never valid; held
	// rather than computed, so no caller sees an underflow
	if predSlot == 0 || succSlot <= predSlot {
		return predB, predC
	}
	gap := uint64(succSlot - predSlot)
	if gap == 1 {
		if predC+1 == c.MineHardenAfter {
			return min(predB+1, c.MineMaxDifficulty), 0
		}
		return predB, predC + 1
	}
	if predB <= c.MineFloorDifficulty+gap-1 {
		return c.MineFloorDifficulty, predC
	}
	return predB - (gap - 1), predC
}

// Mine proof of work, as mineLock reads it from the consumed mine output's
// unlock parameters at the lock element: an ECVRF proof (RFC 9381,
// Gamma || c || s) followed by the nonce. The VRF message binds the work to
// one transit and one target slot. The wallet side needs only the byte layout;
// proving and verifying live in util/vrf, which stays out of this package.
const (
	MineVRFProofLen     = 80
	MineNonceLen        = 8
	MineUnlockParamsLen = MineVRFProofLen + MineNonceLen
)

// MineVRFMessage is alpha = predecessor output ID || slot (4 bytes big-endian,
// as EasyFL txSlot returns it) || nonce.
func MineVRFMessage(pred base.OutputID, slot uint32, nonce [MineNonceLen]byte) []byte {
	ret := make([]byte, 0, base.OutputIDLength+4+MineNonceLen)
	ret = append(ret, pred[:]...)
	ret = binary.BigEndian.AppendUint32(ret, slot)
	return append(ret, nonce[:]...)
}

// MineUnlockParams packs proof || nonce for the mine output's lock element.
func MineUnlockParams(proof []byte, nonce [MineNonceLen]byte) []byte {
	if len(proof) != MineVRFProofLen {
		panic(fmt.Sprintf("MineUnlockParams: proof must be %d bytes, got %d", MineVRFProofLen, len(proof)))
	}
	return append(append(make([]byte, 0, MineUnlockParamsLen), proof...), nonce[:]...)
}
