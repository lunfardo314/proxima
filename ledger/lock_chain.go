package ledger

import (
	"encoding/hex"
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type ChainLock []byte

const (
	//ChainLockName     = "chainLock"
	ChainLockName     = "c"
	chainLockTemplate = ChainLockName + "(0x%s)"
)

//go:embed lock_chain.efl
var chainLockConstraintSource string

var NilChainLock = ChainLockFromChainID(base.NilChainID)

func ChainLockFromChainID(chainID base.ChainID) ChainLock {
	ret := make([]byte, base.ChainIDLength)
	copy(ret, chainID[:])
	return ret
}

// ChainLockFromBytesWithLib parses a ChainLock using the provided library.
// This is the core implementation that avoids repeated L(slot) calls.
func ChainLockFromBytesWithLib(data []byte, lib *Library) (ChainLock, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != ChainLockName {
		return nil, fmt.Errorf("not a ChainLock")
	}
	chainIdBin := easyfl.StripDataPrefix(args[0])

	chainID, err := base.ChainIDFromBytes(chainIdBin)
	if err != nil {
		return nil, err
	}
	return ChainLockFromChainID(chainID), nil
}

// ChainLockFromBytesAtSlot parses a ChainLock using the library for the given slot.
func ChainLockFromBytesAtSlot(data []byte, slot uint32) (ChainLock, error) {
	return ChainLockFromBytesWithLib(data, L(slot))
}

func (cl ChainLock) Source() string {
	return fmt.Sprintf(chainLockTemplate, hex.EncodeToString(cl))
}

func (cl ChainLock) Bytes() []byte {
	return mustBinFromSource(cl.Source())
}

func (cl ChainLock) Accounts() []Accountable {
	return []Accountable{cl}
}

func (cl ChainLock) AccountID() AccountID {
	return cl.Bytes()
}

func (cl ChainLock) Name() string {
	return ChainLockName
}

func (cl ChainLock) String() string {
	return cl.Source()
}

func (cl ChainLock) AsLock() Lock {
	return cl
}

func (cl ChainLock) ChainID() base.ChainID {
	ret, err := base.ChainIDFromBytes(cl)
	util.AssertNoError(err)
	return ret
}

func (cl ChainLock) Master() Accountable {
	return cl
}

func NewChainLockUnlockParams(predChainOutputIndex, predChainConstraintIndex byte) []byte {
	return []byte{predChainOutputIndex, predChainConstraintIndex}
}

func registerChainLockConstraint(lib *Library) {
	lib.mustRegisterConstraint(ChainLockName, 1, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return ChainLockFromBytesWithLib(data, lib)
	})
	lib.mustRegisterLockSerde(ChainLockName, func(bytes []byte) (Lock, error) {
		// Use latest library version for library registration parsing
		ret, err := ChainLockFromBytesWithLib(bytes, lib)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		example := NilChainLock
		chainLockBack, err := ChainLockFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(EqualConstraints(chainLockBack, NilChainLock), "inconsistency "+ChainLockName)

		_, err = lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
	})
}
