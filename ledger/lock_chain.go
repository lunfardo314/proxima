package ledger

import (
	"encoding/hex"
	"fmt"
	"sync"

	_ "embed"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// ChainLock is a typed wrapper for the chain ID (ChainIDLength bytes). The output
// element at index 1 (index-value tuple) of a chain-locked output is
// (chainID); the bytecode at index 2 is the per-kind constant
// `chainLock` 0-arg call (ChainLockBytecode).
type ChainLock []byte

const ChainLockName = "chainLock"

//go:embed def/lock_chain.easyfl
var chainLockConstraintSource string

var NilChainLock = ChainLockFromChainID(base.NilChainID)

func ChainLockFromChainID(chainID base.ChainID) ChainLock {
	ret := make([]byte, base.ChainIDLength)
	copy(ret, chainID[:])
	return ret
}

// chainLockBytecode returns the bytecode of the public 0-arg `chainLock`
// constraint at output element index 2. Constant for all chain-locked
// outputs (the chainID lives in the index-value tuple at index 1).
var (
	chainLockBytecodeOnce  sync.Once
	chainLockBytecodeCache []byte
)

func ChainLockBytecode() []byte {
	chainLockBytecodeOnce.Do(func() {
		chainLockBytecodeCache = mustBinFromSource(ChainLockName)
	})
	return chainLockBytecodeCache
}

func (cl ChainLock) String() string {
	return fmt.Sprintf("chainLock(0x%s)", hex.EncodeToString(cl))
}

func (cl ChainLock) ChainID() base.ChainID {
	ret, err := base.ChainIDFromBytes(cl)
	util.AssertNoError(err)
	return ret
}

// IndexValues returns the single chain ID (ChainIDLength bytes) — the
// index-value tuple of a chain-locked output is (chainID).
func (cl ChainLock) IndexValues() [][]byte {
	return [][]byte{[]byte(cl)}
}

func (cl ChainLock) Name() string               { return ChainLockName }
func (cl ChainLock) LockBytecode() []byte       { return ChainLockBytecode() }
func (cl ChainLock) ControllerID() ControllerID { return ControllerID(cl) }

// Source returns the wallet/CLI mini-syntax `chainLock/<64-hex>`.
func (cl ChainLock) Source() string {
	return ChainLockName + "/" + hex.EncodeToString(cl)
}

// NewChainLockUnlockParams creates unlock params for chain lock. 1 byte:
// the input index of the consumed chain output.
func NewChainLockUnlockParams(predChainOutputIndex byte) []byte {
	return []byte{predChainOutputIndex}
}

func registerChainLockConstraint(lib *Library) {
	lib.registerLockKind(ChainLockName)
}
