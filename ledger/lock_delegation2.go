package ledger

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync/atomic"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type DelegationLock2 struct {
	TargetLock Accountable
	MasterLock Accountable
	// must point to the sibling chain constraint
	ChainConstraintIndex byte
	MaxLockCoverageSlots byte
	FirstOpenSlot        base.Slot
	StartSlot            base.Slot
	StartAmount          uint64
}

const (
	DelegationLock2Name       = "delegationLock2"
	delegationLock2TemplateHR = DelegationLock2Name + "(chainIdx=%d, target=%s, master=%s, maxCoverageLockSlots=%d, startSlot=%d, startAmount=%s, 1stOpenSlot=%d)"
	delegationLock2Template   = DelegationLock2Name + "(%d, %s, %s, %d, z64/%d, z32/%d, z32/%d)"
)

func NewDelegationLock2(chainConstraintIndex byte, owner, target Accountable, maxCoverageLockSlots byte, startSlot base.Slot, startAmount uint64) *DelegationLock2 {
	return &DelegationLock2{
		TargetLock:           target,
		MasterLock:           owner,
		ChainConstraintIndex: chainConstraintIndex,
		StartSlot:            startSlot,
		StartAmount:          startAmount,
		FirstOpenSlot:        0,
		MaxLockCoverageSlots: maxCoverageLockSlots,
	}
}

func DelegationLock2FromBytes(data []byte) (*DelegationLock2, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 7)
	if err != nil {
		return nil, fmt.Errorf("DelegationLock2FromBytes: %w", err)
	}
	if sym != DelegationLock2Name {
		return nil, fmt.Errorf("DelegationLock2FromBytes: not a DelegationLock2")
	}
	// chain constraint index
	arg0 := easyfl.StripDataPrefix(args[0])
	ret := &DelegationLock2{}
	if len(arg0) != 1 || arg0[0] == 255 {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong chain constraint index")
	}
	ret.ChainConstraintIndex = arg0[0]

	// target lock
	ret.TargetLock, err = AccountableFromBytes(args[1])
	if err != nil {
		return nil, fmt.Errorf("DelegationLock2FromBytes: %w", err)
	}
	// master lock
	ret.MasterLock, err = AccountableFromBytes(args[2])
	if err != nil {
		return nil, fmt.Errorf("DelegationLock2FromBytes: %w", err)
	}

	// max coverage lock slots
	arg3 := easyfl.StripDataPrefix(args[3])
	if len(arg3) != 1 {
		return nil, fmt.Errorf("DelegationLock2FromBytes: wrong max coverage lock slots")
	}
	ret.MaxLockCoverageSlots = arg3[0]

	// start slot
	startSlot64, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[4]))
	if err != nil {
		return nil, fmt.Errorf("DelegationLock2FromBytes: %w", err)
	}
	if startSlot64 >= base.MaxSlot {
		return nil, fmt.Errorf("DelegationLock2FromBytes: start slot %d out of range", startSlot64)
	}
	ret.StartSlot = base.Slot(startSlot64)

	// start amount
	ret.StartAmount, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[5]))
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong start amount")
	}
	// coverage locked until slot
	locCov, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[6]))
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong 1st open slot data")
	}
	if locCov >= base.MaxSlot {
		return nil, fmt.Errorf("DelegationLockFromBytes: '1st open slot' is out of range")
	}
	ret.FirstOpenSlot = base.Slot(locCov)
	return ret, nil
}

func (d *DelegationLock2) Source() string {
	return fmt.Sprintf(delegationLock2Template,
		d.ChainConstraintIndex, d.TargetLock.Source(), d.MasterLock.Source(), d.MaxLockCoverageSlots, d.StartSlot, d.StartAmount, d.FirstOpenSlot)
}

func (d *DelegationLock2) String() string {
	return fmt.Sprintf(delegationLock2TemplateHR,
		d.ChainConstraintIndex, d.TargetLock.Source(), d.MasterLock.Source(), d.MaxLockCoverageSlots, d.StartSlot, util.Th(d.StartAmount), d.FirstOpenSlot)
}

func (d *DelegationLock2) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegationLock2) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.TargetLock, d.MasterLock})
}

func (d *DelegationLock2) Name() string {
	return DelegationLock2Name
}

func (d *DelegationLock2) Master() Accountable {
	return d.MasterLock
}

func registerDelegationLock2(lib *Library) {
	lib.mustRegisterConstraint(DelegationLock2Name, 7, func(data []byte) (Constraint, error) {
		return DelegationLock2FromBytes(data)
	}, initTestDelegation2Constraint)
	lib.mustRegisterLock(DelegationLock2Name, func(bytes []byte) (Lock, error) {
		ret, err := DelegationLock2FromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func initTestDelegation2Constraint() {
	a1 := AddressED25519Random()
	a2 := AddressED25519Random()
	slotNow := TimeNow().Slot
	example := NewDelegationLock2(4, a1, a2, 3, slotNow, 1337)
	example.FirstOpenSlot = base.Slot(10000)

	exampleBack, err := DelegationLock2FromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.ChainConstraintIndex == 4, "DelegationLock2FromBytes: wrong back")
	util.Assertf(exampleBack.ChainConstraintIndex == example.ChainConstraintIndex, "DelegationLock2FromBytes: wrong back")
	util.Assertf(example.MaxLockCoverageSlots == 3, "DelegationLock2FromBytes: wrong back")
	util.Assertf(exampleBack.MaxLockCoverageSlots == example.MaxLockCoverageSlots, "DelegationLock2FromBytes: wrong back")
	util.Assertf(example.StartSlot == slotNow, "DelegationLock2FromBytes: wrong back")
	util.Assertf(exampleBack.StartSlot == example.StartSlot, "DelegationLock2FromBytes: wrong back")
	util.Assertf(example.StartAmount == 1337, "DelegationLock2FromBytes: wrong back")
	util.Assertf(example.StartAmount == exampleBack.StartAmount, "DelegationLock2FromBytes: wrong back")
	util.Assertf(example.FirstOpenSlot == 10000, "DelegationLock2FromBytes: wrong back")
	util.Assertf(example.FirstOpenSlot == exampleBack.FirstOpenSlot, "DelegationLock2FromBytes: wrong back")

	util.Assertf(EqualConstraints(example, exampleBack), "inconsistency 1 "+DelegationLock2Name)
	exampleBack2, err := LockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(example, exampleBack2), "inconsistency 2 "+DelegationLock2Name)

	pref1, err := L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)

	pref2, err := L().EvalFromSource(nil, "#delegationLock2")
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(pref1, pref2), "bytes.Equal(pref1, pref2)")
	util.Assertf(example.Source() == exampleBack.Source(), "example.Source()==exampleBack.Source()")
}

var (
	_delegationEpochSlots atomic.Uint64
	_safeRevocationSlots  atomic.Uint64
)

func DelegationEpochSlots() int {
	if ret := _delegationEpochSlots.Load(); ret != 0 {
		return int(ret)
	}
	_precalcDelegationConstants()
	return DelegationEpochSlots()
}

func DelegationSafeRevocationSlots() int {
	if ret := _safeRevocationSlots.Load(); ret != 0 {
		return int(ret)
	}
	_precalcDelegationConstants()
	return DelegationSafeRevocationSlots()
}

func _precalcDelegationConstants() {
	res, err := L().EvalFromSource(nil, "constDelegationEpochSlots")
	util.AssertNoError(err)
	_delegationEpochSlots.Store(binary.BigEndian.Uint64(res))

	res, err = L().EvalFromSource(nil, "constDelegationSafeRevocationSlots")
	util.AssertNoError(err)
	_safeRevocationSlots.Store(binary.BigEndian.Uint64(res))

}

const delegationLock2Source = `
func constDelegationEpochSlots : u64/512
func constDelegationSafeRevocationSlots  : u64/24
func constDelegationMaxLockEpochs : u64/4

// $0 chain ID
func delegationEpoch64 : div(slice($0,0,7), constDelegationEpochSlots)

// Enforces delegation target lock and additional constraints, such as immutable chain 
// transition with non-decreasing amount
// $0 chain constraint index
// $1 target lock
// $2 path to successor output
func _enforceDelegation2TargetConstraintsOnSuccessor : and(
    $1,  // target lock must be unlocked
    require(lessOrEqualThan(selfAmountValue, amountValueByOutputPath($2)), !!!amount_should_not_decrease),
    require(equal(atPath(concat($2, lockConstraintIndex)), selfSiblingConstraint(lockConstraintIndex)), !!!lock_must_be_immutable),
    require(equal(byte(selfSiblingUnlockParams($0),2), 0), !!!chain_must_be_state_transition)
)


// $0 chain constraint index
// $1 target lock
// $2 master lock
// $3 max locked coverage slots
// $4 start slot 
// $5 start amount
// $6 1st open slot
func delegationLock2: and(
    or(
       and(
          selfIsProducedOutput,
  	      //require( and( equalUint(len($0),1), equalUint(len($3),1)), !!!wrong_arg_sizes ), 
          require(not(isBranchTransaction), !!!delegation_should_not_be_branch),
	      enforceMinimumStorageDeposit,
          delegationEpoch64(chainID(selfChainData($0))),
          concat($0,$1,$2,$3,$4,$5,$6)
       ),
       and(
          selfIsConsumedOutput,
          require(greaterOrEqualThan(uint8Bytes($6), uint8Bytes(txSlot)), !!!delegation_lock_is_locked),  // check if not locked
          require(_enforceDelegation2TargetConstraintsOnSuccessor(
                      $0,
                      $1, 
                      concat(pathToProducedOutputs, byte(selfSiblingUnlockParams($0), 0)),  // TODO
                   ), !!!wrong_delegation_target_successor)
       ),
    )
)
`
