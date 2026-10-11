package ledger

import (
	_ "embed"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
)

// MineLock is the fair-launch mine chain lock. It occupies the lock element (output index 2) of the single genesis mine chain
// UTXO and enforces the whole mining policy. It is an OPEN lock (anyone can
// spend, no per-output signature). All of its state is mutable and carried in
// the bytecode; the fixed policy (A, E, C, P, the harden count, the fee) lives
// in ledger constants.
//
//	R  remaining mintable motes (decreases by A each transit)
//	B  current difficulty in bits (seeded from constMineBaseDifficulty)
//	C  full slots in a row since the last harden
//	V  version of the miner that built the output; the covenant requires the
//	   ledger's constMinerVersion, so an outdated miner's transit is invalid
//	O  payout option of the transit that produced the output, 1 opening, 2 new
//	   delegation, 3 full; 0 on genesis (kb/mine_payout_options.md)
//
// The retarget reacts to the single last gap (successor slot - predecessor
// slot): one bit harder after constMineHardenAfter full slots in a row, one
// bit easier per empty slot.
type MineLock struct {
	R uint64
	B uint64
	C uint64
	V uint64
	O uint64
}

const MineLockName = "mineLock"

// MineLockTemplate: args are (R, B, C, V, O).
const MineLockTemplate = MineLockName + "(z64/%d, z64/%d, z64/%d, z64/%d, z64/%d)"

//go:embed def/lock_mine.easyfl
var mineLockSource string

func NewMineLock(r, b, c, v, o uint64) *MineLock {
	return &MineLock{R: r, B: b, C: c, V: v, O: o}
}

// Source returns the 5-arg mineLock EasyFL source.
func (m *MineLock) Source() string {
	return fmt.Sprintf(MineLockTemplate, m.R, m.B, m.C, m.V, m.O)
}

func (m *MineLock) String() string {
	return fmt.Sprintf("mineLock(R=%d, B=%d, C=%d, V=%d, O=%d)", m.R, m.B, m.C, m.V, m.O)
}

func (m *MineLock) Bytes() []byte        { return mustBinFromSource(m.Source()) }
func (m *MineLock) Name() string         { return MineLockName }
func (m *MineLock) LockBytecode() []byte { return m.Bytes() }

// IndexValues is empty: the mine output carries no index-value tuple entries;
// all mineLock state lives in the lock bytecode.
func (m *MineLock) IndexValues() [][]byte { return nil }

// MineLockFromBytesWithLib parses the 5-arg mineLock bytecode at output element
// index 2.
func MineLockFromBytesWithLib(data []byte, lib *Library) (*MineLock, error) {
	sym, _, args, err := lib.Library.ParseBytecodeOneLevel(data, 5)
	if err != nil {
		return nil, fmt.Errorf("MineLockFromBytes: %w", err)
	}
	if sym != MineLockName {
		return nil, fmt.Errorf("MineLockFromBytes: not a MineLock")
	}
	ret := &MineLock{}
	if ret.R, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0])); err != nil {
		return nil, fmt.Errorf("MineLockFromBytes: wrong R: %w", err)
	}
	if ret.B, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1])); err != nil {
		return nil, fmt.Errorf("MineLockFromBytes: wrong B: %w", err)
	}
	if ret.C, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2])); err != nil {
		return nil, fmt.Errorf("MineLockFromBytes: wrong C: %w", err)
	}
	if ret.V, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3])); err != nil {
		return nil, fmt.Errorf("MineLockFromBytes: wrong V: %w", err)
	}
	if ret.O, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[4])); err != nil {
		return nil, fmt.Errorf("MineLockFromBytes: wrong O: %w", err)
	}
	return ret, nil
}

func registerMineLock(lib *Library) {
	lib.mustRegisterConstraint(MineLockName, 5, func(data []byte) (Constraint, error) {
		return MineLockFromBytesWithLib(data, lib)
	})
}
