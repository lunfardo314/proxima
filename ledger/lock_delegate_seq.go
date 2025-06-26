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
	DelegateToSequencerLock struct {
		Target     ChainLock
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
	DelegateToSequencerLockName       = "delegateToSequencerLock"
	delegateToSequencerLockTemplate   = DelegateToSequencerLockName + "(%d, %s, %s, %d, z64/%d, z32/%d)"
	delegateToSequencerLockTemplateHR = DelegateToSequencerLockName + "(chainIdx=%d, target=%s, master=%s, maxCoverageLockSlots=%d, startSlot=%d, startAmount=%s)"

	FreezeDelegationLockName       = "freezeDelegationLock"
	delegationLockFreezeTemplate   = FreezeDelegationLockName + "(z32/%d)"
	delegationLockFreezeTemplateHR = FreezeDelegationLockName + "(freezeSlot=%d)"
)

//------------ DelegateToSequencerLock

func NewDelegateToSequencerLock(chainConstraintIndex byte, target ChainLock, master Accountable, maxCoverageLockSlots byte, startSlot base.Slot, startAmount uint64) *DelegateToSequencerLock {
	return &DelegateToSequencerLock{
		Target:               target,
		MasterLock:           master,
		ChainConstraintIndex: chainConstraintIndex,
		StartSlot:            startSlot,
		StartAmount:          startAmount,
		MaxLockCoverageSlots: maxCoverageLockSlots,
	}
}

func (d *DelegateToSequencerLock) Source() string {
	return fmt.Sprintf(delegateToSequencerLockTemplate,
		d.ChainConstraintIndex, d.Target.Source(), d.MasterLock.Source(), d.MaxLockCoverageSlots, d.StartSlot, d.StartAmount)
}

func (d *DelegateToSequencerLock) String() string {
	return fmt.Sprintf(delegateToSequencerLockTemplateHR,
		d.ChainConstraintIndex, d.Target.String(), d.MasterLock.String(), d.MaxLockCoverageSlots, d.StartSlot, util.Th(d.StartAmount))
}

func (d *DelegateToSequencerLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegateToSequencerLock) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.Target, d.MasterLock})
}

func DelegateToSequencerLockFromBytes(data []byte) (*DelegateToSequencerLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 6)
	if err != nil {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: %w", err)
	}
	if sym != DelegateToSequencerLockName {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: not a DelegateToSequencerLock")
	}
	// chain constraint index
	arg0 := easyfl.StripDataPrefix(args[0])
	ret := &DelegateToSequencerLock{}
	if len(arg0) != 1 || arg0[0] == 255 {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: wrong chain constraint index")
	}
	ret.ChainConstraintIndex = arg0[0]

	// target lock
	ret.Target, err = ChainLockFromBytes(args[1])
	if err != nil {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: %w", err)
	}
	// master lock
	ret.MasterLock, err = AccountableFromBytes(args[2])
	if err != nil {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: %w", err)
	}

	// max coverage lock slots
	arg3 := easyfl.StripDataPrefix(args[3])
	if len(arg3) != 1 {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: wrong max coverage lock slots")
	}
	ret.MaxLockCoverageSlots = arg3[0]

	// start slot
	startSlot64, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[4]))
	if err != nil {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: %w", err)
	}
	if startSlot64 >= base.MaxSlot {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: start slot %d out of range", startSlot64)
	}
	ret.StartSlot = base.Slot(startSlot64)

	// start amount
	ret.StartAmount, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[5]))
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong start amount")
	}
	return ret, nil
}

func (d *DelegateToSequencerLock) Name() string {
	return DelegateToSequencerLockName
}

func (d *DelegateToSequencerLock) Master() Accountable {
	return d.MasterLock
}

func registerDelegationLock2(lib *Library) {
	lib.mustRegisterConstraint(DelegateToSequencerLockName, 6, func(data []byte) (Constraint, error) {
		return DelegateToSequencerLockFromBytes(data)
	}, initTestDelegation2Constraint)
	lib.mustRegisterLock(DelegateToSequencerLockName, func(bytes []byte) (Lock, error) {
		ret, err := DelegateToSequencerLockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func initTestDelegation2Constraint() {
	target := ChainLockFromChainID(base.RandomChainID())
	master := AddressED25519Random()
	slotNow := TimeNow().Slot
	example := NewDelegateToSequencerLock(4, target, master, 3, slotNow, 1337)

	exampleBack, err := DelegateToSequencerLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.ChainConstraintIndex == 4, "DelegateToSequencerLockFromBytes: wrong back")
	util.Assertf(exampleBack.ChainConstraintIndex == example.ChainConstraintIndex, "DelegateToSequencerLockFromBytes: wrong back")
	util.Assertf(example.MaxLockCoverageSlots == 3, "DelegateToSequencerLockFromBytes: wrong back")
	util.Assertf(exampleBack.MaxLockCoverageSlots == example.MaxLockCoverageSlots, "DelegateToSequencerLockFromBytes: wrong back")
	util.Assertf(example.StartSlot == slotNow, "DelegateToSequencerLockFromBytes: wrong back")
	util.Assertf(exampleBack.StartSlot == example.StartSlot, "DelegateToSequencerLockFromBytes: wrong back")
	util.Assertf(example.StartAmount == 1337, "DelegateToSequencerLockFromBytes: wrong back")
	util.Assertf(example.StartAmount == exampleBack.StartAmount, "DelegateToSequencerLockFromBytes: wrong back")

	util.Assertf(EqualConstraints(example, exampleBack), "inconsistency 1 "+DelegateToSequencerLockName)
	exampleBack2, err := LockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(example, exampleBack2), "inconsistency 2 "+DelegateToSequencerLockName)

	pref1, err := L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)

	pref2, err := L().EvalFromSource(nil, "#"+DelegateToSequencerLockName)
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
// $1 target chain lock
// $2 master lock
// $3 max freeze epochs
// $4 start slot 
// $5 start amount
func delegateToSequencerLock: and(
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
func selfChainIDFromDelegation : chainID(selfChainData(parseInlineDataArgument(selfSiblingConstraint(1), 0, #delegateToSequencerLock)))

func selfDelegationEpochOffset : div(slice(selfChainIDFromDelegation,0,7), constDelegationEpochSlots)

func selfMaxFreezeEpochs64 : uint8Bytes(parseInlineDataArgument(selfSiblingConstraint(1), 3, #delegateToSequencerLock))

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
		return 0, fmt.Errorf("DelegationLockFreezeFromBytes: not a DelegateToSequencerLock")
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
)

func revoked : true
`
