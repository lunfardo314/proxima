package ledger

import (
	"encoding/hex"
	"fmt"

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

var NilChainLock = ChainLockFromChainID(base.NilChainID)

func ChainLockFromChainID(chainID base.ChainID) ChainLock {
	ret := make([]byte, base.ChainIDLength)
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

	chainID, err := base.ChainIDFromBytes(chainIdBin)
	if err != nil {
		return nil, err
	}
	return ChainLockFromChainID(chainID), nil
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
		return ChainLockFromBytes(data)
	}, initTestChainLockConstraint)
	lib.mustRegisterLock(ChainLockName, func(bytes []byte) (Lock, error) {
		ret, err := ChainLockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
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
	parseInlineDataArgument(
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
// $1 - self unlock parameters
func validChainUnlock : and(
    equal(len($1), u64/2),                          // prevent panic in compound locks
	equal($0, selfReferencedChainIDAdjusted(slice(selfReferencedChainData,0,31))), // chain id must be equal to the referenced chain id 
	equal(
		// the chain must be unlocked for state transition (mode = 0) 
		byte(unlockParamsByConstraintIndex($1),2),
		0
	)
)

// $0 - chainID
// Unlock parameters 2 bytes: [unlocked chain output index, chain constraint index]
func chainLock : and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	enforceMinimumStorageDeposit,
	or(
		and(
			selfIsProducedOutput, 
			require(equal(len($0),u64/32), !!!32-byte_long_argument_expected),
            require(not(isZero($0)), !!!non_zero_argument_expected)   // to prevent common error
		),
		and(
			selfIsConsumedOutput,
			not(equal(selfOutputIndex, byte(selfUnlockParameters,0))), // prevent self referencing 
			validChainUnlock($0, selfUnlockParameters)
		)
	)
)

// short version of chainLock
// $0 - chainID
// Unlock parameters 2 bytes: [unlocked chain output index, chain constraint index]
func c : chainLock($0)
`
