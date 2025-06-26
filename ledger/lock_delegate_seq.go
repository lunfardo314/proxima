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
		MaxLockCoverageSlots byte
		StartSlot            base.Slot
		StartAmount          uint64
	}

	DelegateToSequencerLockState struct {
		State byte
	}
)

const (
	DelegateToSequencerLockStateInit    = byte(0)
	DelegateToSequencerLockStateFrozen  = byte(1)
	DelegateToSequencerLockStateRevoked = byte(2)
)

const (
	DelegateToSequencerLockName       = "delegateToSequencerLock"
	delegateToSequencerLockTemplate   = DelegateToSequencerLockName + "(%s, %s, %d, z64/%d, z32/%d)"
	delegateToSequencerLockTemplateHR = DelegateToSequencerLockName + "(target=%s, master=%s, maxCoverageLockSlots=%d, startSlot=%d, startAmount=%s)"

	delegateToSequencerLockStateName       = "delegateToSequencerLockState"
	delegateToSequencerLockStateTemplate   = delegateToSequencerLockStateName + "(z32/%d)"
	delegateToSequencerLockStateTemplateHR = delegateToSequencerLockStateName + "(state=%d)"
)

//------------ DelegateToSequencerLock

func NewDelegateToSequencerLock(target ChainLock, master Accountable, maxCoverageLockSlots byte, startSlot base.Slot, startAmount uint64) *DelegateToSequencerLock {
	return &DelegateToSequencerLock{
		Target:               target,
		MasterLock:           master,
		StartSlot:            startSlot,
		StartAmount:          startAmount,
		MaxLockCoverageSlots: maxCoverageLockSlots,
	}
}

func (d *DelegateToSequencerLock) Source() string {
	return fmt.Sprintf(delegateToSequencerLockTemplate, d.Target.Source(), d.MasterLock.Source(), d.MaxLockCoverageSlots, d.StartSlot, d.StartAmount)
}

func (d *DelegateToSequencerLock) String() string {
	return fmt.Sprintf(delegateToSequencerLockTemplateHR, d.Target.String(), d.MasterLock.String(), d.MaxLockCoverageSlots, d.StartSlot, util.Th(d.StartAmount))
}

func (d *DelegateToSequencerLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegateToSequencerLock) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.Target, d.MasterLock})
}

func DelegateToSequencerLockFromBytes(data []byte) (*DelegateToSequencerLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 5)
	if err != nil {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: %w", err)
	}
	if sym != DelegateToSequencerLockName {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: not a DelegateToSequencerLock")
	}
	// chain constraint index
	ret := &DelegateToSequencerLock{}

	// target lock
	ret.Target, err = ChainLockFromBytes(args[0])
	if err != nil {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: %w", err)
	}
	// master lock
	ret.MasterLock, err = AccountableFromBytes(args[1])
	if err != nil {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: %w", err)
	}

	// max coverage lock slots
	arg2 := easyfl.StripDataPrefix(args[2])
	if len(arg2) != 1 {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: wrong max coverage lock slots")
	}
	ret.MaxLockCoverageSlots = arg2[0]

	// start slot
	startSlot64, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: %w", err)
	}
	if startSlot64 >= base.MaxSlot {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: start slot %d out of range", startSlot64)
	}
	ret.StartSlot = base.Slot(startSlot64)

	// start amount
	ret.StartAmount, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[4]))
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

func registerDelegateToSequencerLock(lib *Library) {
	lib.mustRegisterConstraint(DelegateToSequencerLockName, 5, func(data []byte) (Constraint, error) {
		return DelegateToSequencerLockFromBytes(data)
	}, initTestDelegateToSequencerConstraint)
	lib.mustRegisterLock(DelegateToSequencerLockName, func(bytes []byte) (Lock, error) {
		ret, err := DelegateToSequencerLockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
	lib.mustRegisterConstraint(delegateToSequencerLockStateName, 1, func(data []byte) (Constraint, error) {
		return DelegateToSequencerLockStateFromBytes(data)
	}, initTestDelegateToSequencerLockState)

}

func initTestDelegateToSequencerConstraint() {
	target := ChainLockFromChainID(base.RandomChainID())
	master := AddressED25519Random()
	slotNow := TimeNow().Slot
	example := NewDelegateToSequencerLock(target, master, 3, slotNow, 1337)

	exampleBack, err := DelegateToSequencerLockFromBytes(example.Bytes())
	util.AssertNoError(err)
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

//--------------------------- delegationLockFreeze

func NewDelegateToSequencerLockState(state byte) DelegateToSequencerLockState {
	return DelegateToSequencerLockState{state}
}

func DelegateToSequencerLockStateFromBytes(data []byte) (DelegateToSequencerLockState, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return DelegateToSequencerLockState{}, fmt.Errorf("DelegateToSequencerLockStateFromBytes: %w", err)
	}
	if sym != delegateToSequencerLockStateName {
		return DelegateToSequencerLockState{}, fmt.Errorf("DelegateToSequencerLockStateFromBytes: not a DelegateToSequencerLockState")
	}
	arg0 := easyfl.StripDataPrefix(args[0])
	if len(arg0) > 1 {
		return DelegateToSequencerLockState{}, fmt.Errorf("DelegateToSequencerLockStateFromBytes: wrong argument 0: %w", err)
	}
	s := byte(0)
	if len(arg0) == 1 {
		s = arg0[0]
	}
	return DelegateToSequencerLockState{s}, nil
}

func (d DelegateToSequencerLockState) Source() string {
	return fmt.Sprintf(delegateToSequencerLockStateTemplate, d.State)
}

func (d DelegateToSequencerLockState) String() string {
	return fmt.Sprintf(delegateToSequencerLockStateTemplateHR, d.State)
}

func (d DelegateToSequencerLockState) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegateToSequencerLockState) Name() string {
	return delegateToSequencerLockStateName
}

func initTestDelegateToSequencerLockState() {
	dlz := NewDelegateToSequencerLockState(DelegateToSequencerLockStateFrozen)

	dlzBack, err := DelegateToSequencerLockStateFromBytes(dlz.Bytes())
	util.AssertNoError(err)
	util.Assertf(dlzBack.State == DelegateToSequencerLockStateFrozen, "DelegateToSequencerLockState: inconsistency 1")
	util.Assertf(dlz == dlzBack, "DelegateToSequencerLockState: inconsistency 2")
}

const delegateToSequencerLockSource = `
func constDelegationEpochSlotsShiftBits : u64/9
func constDelegationEpochSlots : lshift64(u64/1, constDelegationEpochSlotsShiftBits)
func constDelegationSafeRevocationSlots  : u64/24
func constDelegationMaxLockEpochs : u64/4

// $0 index of the chain constraint in the consumed output
func pathToSuccessorOutput : concat(pathToProducedOutputs, byte(selfSiblingUnlockParams($0), 0))

// $0 index of the chain constraint on the predecessor (consumed output)
func successorConstraint : atPath(concat(pathToSuccessorOutput($0), lockConstraintIndex))

// Enforces delegation target lock and additional constraints: immutable chain transition with non-decreasing amount
// $0 target lock
// $1 path to successor
func _enforceDelegation2TargetConstraintsOnSuccessor : and(
    $0,  // target lock must be unlocked
    require(lessOrEqualThan(selfAmountValue, amountValueByOutputPath($1)), !!!amount_should_not_decrease),
    require(equal(successorConstraint(2), selfSiblingConstraint(lockConstraintIndex)), !!!delegation_lock_must_be_immutable),
    require(equal(byte(selfSiblingUnlockParams(2),2), 0), !!!chain_must_be_state_transition)
)

func delegateToSequencerLockState : or($0,true)

func _delegationState : parseInlineDataArgument(selfSiblingConstraint(3),#delegateToSequencerLockState, 0)
func _isInitDelegationState : isZero(_delegationState)

// Delegation lock output. Immutable 
// $0 target chain lock
// $1 master lock
// $2 max freeze epochs
// $3 start slot 
// $4 start amount
func delegateToSequencerLock: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
    or(
       and(
          selfIsProducedOutput,
	      enforceMinimumStorageDeposit,
          require(
             equal(selfNumConstraints, u64/4), 
             !!!delegation_must_have_4_constraints
          ), // prevent attacks
          require(
             not(isBranchTransaction), 
             !!!delegation_should_not_be_branch
          ),
          require(
             equal(parsePrefixBytecode(selfSiblingConstraint(2)), #chain), 
             !!!#chain_is_expected_at_index_2
          ),
          require(
             equal(parsePrefixBytecode(selfSiblingConstraint(3)), #delegateToSequencerLockState), 
             !!!#delegateToSequencerLockState_is_expected_at_index_3
          ),
          require(
             or(not(_isInitDelegationState), equalUint(txSlot,$3)), 
             !!!wrong_start_slot
          ),
          require(
             or(not(_isInitDelegationState), equalUint(selfAmountValue,$4)), 
             !!!wrong_start_amount
          ),
       ),
       and(
          selfIsConsumedOutput,
          require(_enforceDelegation2TargetConstraintsOnSuccessor(
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

//----------------------------------------------------
// $0 frozen slot
// ($0>>9 + 'max freeze slots') << 9
func _unfreezeSlot : lshift64(add(rshift64($0,constDelegationEpochSlotsShiftBits), selfMaxFreezeEpochs64), constDelegationEpochSlotsShiftBits)

`
