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

type (
	DelegationLock2 struct {
		TargetLock Accountable
		MasterLock Accountable
		// must point to the sibling chain constraint
		ChainConstraintIndex byte
		MaxLockCoverageSlots byte
		StartSlot            base.Slot
		StartAmount          uint64
	}

	DelegationLockFreeze base.Slot
)

const (
	DelegationLock2Name       = "delegationLock2"
	delegationLock2Template   = DelegationLock2Name + "(%d, %s, %s, %d, z64/%d, z32/%d)"
	delegationLock2TemplateHR = DelegationLock2Name + "(chainIdx=%d, target=%s, master=%s, maxCoverageLockSlots=%d, startSlot=%d, startAmount=%s)"

	FreezeDelegationLockName       = "freezeDelegationLock"
	delegationLockFreezeTemplate   = FreezeDelegationLockName + "(z32/%d)"
	delegationLockFreezeTemplateHR = FreezeDelegationLockName + "(freezeSlot=%d)"
)

//------------ DelegationLock2

func NewDelegationLock2(chainConstraintIndex byte, owner, target Accountable, maxCoverageLockSlots byte, startSlot base.Slot, startAmount uint64) *DelegationLock2 {
	return &DelegationLock2{
		TargetLock:           target,
		MasterLock:           owner,
		ChainConstraintIndex: chainConstraintIndex,
		StartSlot:            startSlot,
		StartAmount:          startAmount,
		MaxLockCoverageSlots: maxCoverageLockSlots,
	}
}

func DelegationLock2FromBytes(data []byte) (*DelegationLock2, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 6)
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
	return ret, nil
}

func (d *DelegationLock2) Source() string {
	return fmt.Sprintf(delegationLock2Template,
		d.ChainConstraintIndex, d.TargetLock.Source(), d.MasterLock.Source(), d.MaxLockCoverageSlots, d.StartSlot, d.StartAmount)
}

func (d *DelegationLock2) String() string {
	return fmt.Sprintf(delegationLock2TemplateHR,
		d.ChainConstraintIndex, d.TargetLock.Source(), d.MasterLock.Source(), d.MaxLockCoverageSlots, d.StartSlot, util.Th(d.StartAmount))
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
	lib.mustRegisterConstraint(DelegationLock2Name, 6, func(data []byte) (Constraint, error) {
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
func constDelegationEpochSlotsShiftBits : u64/9
func constDelegationEpochSlots : lshift64(u64/1, constDelegationEpochSlotsShiftBits)
func constDelegationSafeRevocationSlots  : u64/24
func constDelegationMaxLockEpochs : u64/4

// $0 index of the chain constraint in the consumed output
func pathToSuccessorOutput : concat(pathToProducedOutputs, byte(selfSiblingUnlockParams($0), 0))

// $0 index of the chain constraint on the predecessor (consumed output)
func successorConstraint : atPath(concat(pathToSuccessorOutput($0), lockConstraintIndex))

// Enforces delegation target lock and additional constraints: immutable chain transition with non-decreasing amount
// $0 chain constraint index
// $1 target lock
func _enforceDelegation2TargetConstraintsOnSuccessor : and(
    $1,  // target lock must be unlocked
    require(lessOrEqualThan(selfAmountValue, amountValueByOutputPath($2)), !!!amount_should_not_decrease),
    require(equal(successorConstraint($0), selfSiblingConstraint(lockConstraintIndex)), !!!delegation_lock_must_be_immutable),
    require(equal(byte(selfSiblingUnlockParams($0),2), 0), !!!chain_must_be_state_transition)
)


// Delegation lock output. Immutable 
// $0 chain constraint index
// $1 target lock
// $2 master lock
// $3 max freeze epochs
// $4 start slot 
// $5 start amount
func delegationLock2: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
    or(
       and(
          selfIsProducedOutput,
  	      // require( and( equalUint(len($0),1), equalUint(len($3),1)), !!!wrong_arg_sizes ), 
          require(not(isBranchTransaction), !!!delegation_should_not_be_branch),
	      enforceMinimumStorageDeposit,
          // delegationEpoch64(chainID(selfChainData($0))),
          concat($0,$1,$2,$3,$4,$5)
       ),
       and(
          selfIsConsumedOutput,
          require(_enforceDelegation2TargetConstraintsOnSuccessor(
             $0,
             $1, 
             concat(pathToProducedOutputs, byte(selfSiblingUnlockParams($0), 0)),  // TODO
          ), !!!wrong_delegation_target_successor)
       ),
    )
)

// chain ID from delegation lock
func selfChainIDFromDelegation : chainID(selfChainData(parseInlineDataArgument(selfSiblingConstraint(1), 0, #delegationLock2)))

func selfDelegationEpochOffset : div(slice(selfChainIDFromDelegation,0,7), constDelegationEpochSlots)

func selfMaxFreezeEpochs64 : uint8Bytes(parseInlineDataArgument(selfSiblingConstraint(1), 3, #delegationLock2))

`

//--------------------------- delegationLockFreeze

func NewFreezeDelegationLock(freeze base.Slot) DelegationLockFreeze {
	return DelegationLockFreeze(freeze)
}

func DelegationLockFreezeFromBytes(data []byte) (DelegationLockFreeze, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return 0, fmt.Errorf("DelegationLockFreezeFromBytes: %w", err)
	}
	if sym != FreezeDelegationLockName {
		return 0, fmt.Errorf("DelegationLockFreezeFromBytes: not a DelegationLock2")
	}
	ret, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return 0, fmt.Errorf("DelegationLockFreezeFromBytes: wrong argument 0: %w", err)
	}
	if ret >= base.MaxSlot {
		return 0, fmt.Errorf("DelegationLockFreezeFromBytes: wrong argument 0")
	}
	return DelegationLockFreeze(ret), nil
}

func (d DelegationLockFreeze) Source() string {
	return fmt.Sprintf(delegationLockFreezeTemplate, d)
}

func (d DelegationLockFreeze) String() string {
	return fmt.Sprintf(delegationLockFreezeTemplateHR, d)
}

func (d DelegationLockFreeze) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegationLockFreeze) Name() string {
	return FreezeDelegationLockName
}

func registerDelegationLockFreeze(lib *Library) {
	lib.mustRegisterConstraint(FreezeDelegationLockName, 1, func(data []byte) (Constraint, error) {
		return DelegationLockFreezeFromBytes(data)
	}, initTestDelegationLockFreezeConstraint)
}

func initTestDelegationLockFreezeConstraint() {
	dlz := NewFreezeDelegationLock(10001)

	dlzBack, err := DelegationLockFreezeFromBytes(dlz.Bytes())
	util.AssertNoError(err)
	util.Assertf(dlzBack == 10001, "DelegationLockFreeze: inconsistency 1")
	util.Assertf(dlz == dlzBack, "DelegationLockFreeze: inconsistency 2")
}

const freezeDelegationLockSource = `
//----------------------------------------------------
// $0 frozen slot
// ($0>>9 + 'max freeze slots') << 9
func _unfreezeSlot : lshift64(add(rshift64($0,constDelegationEpochSlotsShiftBits), selfMaxFreezeEpochs64), constDelegationEpochSlotsShiftBits)

// $0 frozen at slot 
// constraint which freezes delegation output 
//  - from slot $0+1 
//  - until ($0/512 + 'max freeze slots') x 512 
func freezeDelegationLock : 
or(
   and(
	  selfIsProducedOutput,
      require(equalUint(txSlot, $0), !!!wrong_frozen_slot), 
   ),
   and(
	  selfIsConsumedOutput,
	  require(
		 greaterOrEqualThan(uint8Bytes(txSlot), _unfreezeSlot($0)), 
		 !!!delegation2_output_is_frozen
	  )
   )
)`
