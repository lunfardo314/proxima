package ledger

import (
	_ "embed"
	"fmt"
	"math"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
)

// MineLock is the fair-launch mine chain lock (see claude/fairlaunch.md). It
// occupies the lock element (output index 2) of the single genesis mine chain
// UTXO and enforces the whole mining policy. It is an OPEN lock (anyone can
// spend, no per-output signature). All of its state is mutable and carried in
// the bytecode; the fixed policy (A, E, P) lives in ledger constants.
//
//	R  remaining mintable motes (decreases by A each transit)
//	B  current base/max difficulty in bits (seeded from constMineBaseDifficulty)
//	S1 slot of the pre-predecessor          (newest ring entry)
//	S2 slot of the pre-pre-predecessor
//	S3 slot of the pre-pre-pre-predecessor  (oldest ring entry)
type MineLock struct {
	R  uint64
	B  uint64
	S1 uint32
	S2 uint32
	S3 uint32
}

const MineLockName = "mineLock"

// MineLockTemplate: args are (R, B, s3, s2, s1) — ring oldest-to-newest.
const MineLockTemplate = MineLockName + "(z64/%d, z64/%d, z32/%d, z32/%d, z32/%d)"

//go:embed def/lock_mine.easyfl
var mineLockSource string

func NewMineLock(r, b uint64, s1, s2, s3 uint32) *MineLock {
	return &MineLock{R: r, B: b, S1: s1, S2: s2, S3: s3}
}

// Source returns the 5-arg mineLock EasyFL source (all args in the bytecode).
func (m *MineLock) Source() string {
	return fmt.Sprintf(MineLockTemplate, m.R, m.B, m.S3, m.S2, m.S1)
}

func (m *MineLock) String() string {
	return fmt.Sprintf("mineLock(R=%d, B=%d, ring=[s3=%d, s2=%d, s1=%d])", m.R, m.B, m.S3, m.S2, m.S1)
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
	s3, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil || s3 > math.MaxUint32 {
		return nil, fmt.Errorf("MineLockFromBytes: wrong s3: %w", err)
	}
	s2, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil || s2 > math.MaxUint32 {
		return nil, fmt.Errorf("MineLockFromBytes: wrong s2: %w", err)
	}
	s1, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[4]))
	if err != nil || s1 > math.MaxUint32 {
		return nil, fmt.Errorf("MineLockFromBytes: wrong s1: %w", err)
	}
	ret.S3, ret.S2, ret.S1 = uint32(s3), uint32(s2), uint32(s1)
	return ret, nil
}

func registerMineLock(lib *Library) {
	lib.mustRegisterConstraint(MineLockName, 5, func(data []byte) (Constraint, error) {
		return MineLockFromBytesWithLib(data, lib)
	})
}
