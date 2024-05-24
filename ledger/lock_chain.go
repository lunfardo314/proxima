package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

type ChainLock []byte

const (
	ChainLockName     = "chainLock"
	chainLockTemplate = ChainLockName + "(0x%s)"
)

var NilChainLock = ChainLockFromChainID(NilChainID)

func ChainLockFromChainID(chainID ChainID) ChainLock {
	ret := make([]byte, ChainIDLength)
	copy(ret, chainID[:])
	return ret
}

func ChainLockFromBytes(data []byte) (ChainLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != ChainLockName {
		return nil, fmt.Errorf("not a ChainLock")
	}
	chainIdBin := easyfl.StripDataPrefix(args[0])

	chainID, err := ChainIDFromBytes(chainIdBin)
	if err != nil {
		return nil, err
	}
	return ChainLockFromChainID(chainID), nil
}

func (cl ChainLock) source() string {
	return fmt.Sprintf(chainLockTemplate, hex.EncodeToString(cl))
}

func (cl ChainLock) Bytes() []byte {
	return mustBinFromSource(cl.source())
}

func (cl ChainLock) Accounts() []Accountable {
	return []Accountable{cl}
}

func (cl ChainLock) UnlockableWith(acc AccountID, _ ...Time) bool {
	return bytes.Equal(cl.AccountID(), acc)
}

func (cl ChainLock) AccountID() AccountID {
	return cl.Bytes()
}

func (cl ChainLock) Name() string {
	return ChainLockName
}

func (cl ChainLock) String() string {
	return cl.source()
}

func (cl ChainLock) AsLock() Lock {
	return cl
}

func (cl ChainLock) ChainID() ChainID {
	ret, err := ChainIDFromBytes(cl)
	util.AssertNoError(err)
	return ret
}

func NewChainLockUnlockParams(chainOutputIndex, chainConstraintIndex byte) []byte {
	return []byte{chainOutputIndex, chainConstraintIndex}
}

func addChainLockConstraint(lib *Library) {
	lib.extendWithConstraint(ChainLockName, chainLockConstraintSource, 1, func(data []byte) (Constraint, error) {
		return ChainLockFromBytes(data)
	}, initTestChainLockConstraint)
}

func initTestChainLockConstraint() {
	example := NilChainLock
	chainLockBack, err := ChainLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(chainLockBack, NilChainLock), "inconsistency "+ChainLockName)

	_, err = L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)
}

const chainLockConstraintSource = `

func selfReferencedChainData : 
	evalArgumentBytecode(
		consumedConstraintByIndex(selfUnlockParameters),
		#chain,
		0
	)

// $0 - parsed referenced chain constraint
func selfReferencedChainIDAdjusted : if(
	isZero($0),
	blake2b(inputIDByIndex(byte(selfUnlockParameters, 0))),
	$0
)

// $0 - chainID
func validChainUnlock : and(
	equal($0, selfReferencedChainIDAdjusted(slice(selfReferencedChainData,0,31))), // chain id must be equal to the referenced chain id 
	equal(
		// the chain must be unlocked for state transition (mode = 0) 
		byte(unlockParamsByConstraintIndex(selfUnlockParameters),2),
		0
	)
)

func chainLock : and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	selfMustStandardAmount,
	or(
		and(
			selfIsProducedOutput, 
			equal(len($0),u64/32),
            not(isZero($0))   // to prevent common error
		),
		and(
			selfIsConsumedOutput,
			not(equal(selfOutputIndex, byte(selfUnlockParameters,0))), // prevent self referencing 
			validChainUnlock($0)
		)
	)
)

`
