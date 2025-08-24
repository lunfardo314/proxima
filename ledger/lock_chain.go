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

// $0 selfUnlockParameters
func _selfReferencedChainID : 
	parseInlineDataArgument(
		consumedConstraintByIndex(byte($0, 0), byte($0, 1)),
		#chain,
		0
	)

// $0 - unlock params
func _selfReferencedChainIDAdjusted : if(
	isZero(_selfReferencedChainID($0)),
	blake2b(inputIDByIndex(byte($0, 0))),
	_selfReferencedChainID($0)
)

// $0 selfUnlockParameters
func _chainLockUnlock : if( lessThan(len($0), u64/2), 0xffff, slice($0,0,1) )

// $0 - chainID
func _validChainUnlock : 
       // chain id must be equal to the referenced chain id 
   equal($0, _selfReferencedChainIDAdjusted(_chainLockUnlock(selfUnlockParameters))) 


// $0 - chainID
// Unlock parameters first 2 bytes: [unlocked chain output index, chain constraint index]
func chainLock : and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	or(
		and(
			selfIsProducedOutput, 
			require(equal(len($0),u64/32), !!!32-byte_long_argument_expected),
            require(not(isZero($0)), !!!non_zero_argument_expected)   // to prevent common error
		),
		and(
			selfIsConsumedOutput,
			not(equal(selfOutputIndex, byte(selfUnlockParameters,0))), // prevent self referencing 
			_validChainUnlock($0)
		)
	)
)

// short version of chainLock
// $0 - chainID
// Unlock parameters 2 bytes: [unlocked chain output index, chain constraint index]
func c : chainLock($0)
`
