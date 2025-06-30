package ledger

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type (
	DelegateToSequencerLock struct {
		Target         ChainLock
		MasterLock     Accountable
		MaxFreezeSlots uint16
	}
	DelegateToSequencerLockState struct {
		UnfreezeEpoch uint64
		Revoked       bool
	}
)

const (
	DelegateToSequencerLockName       = "delegateToSequencerLock"
	delegateToSequencerLockTemplate   = DelegateToSequencerLockName + "(%s, %s, z16/%d)"
	delegateToSequencerLockTemplateHR = DelegateToSequencerLockName + "(target=%s, master=%s, maxFreezeSlots=%d)"

	delegateToSequencerLockStateName       = "delegateToSequencerLockState"
	delegateToSequencerLockStateTemplate   = delegateToSequencerLockStateName + "(z32/%d, %s)"
	delegateToSequencerLockStateTemplateHR = delegateToSequencerLockStateName + "(unfreezeEpoch=%d, revoked=%v)"
)

//------------ DelegateToSequencerLock

func NewDelegateToSequencerLock(target ChainLock, master Accountable, maxFreezeSlots uint16) *DelegateToSequencerLock {
	return &DelegateToSequencerLock{
		Target:         target,
		MasterLock:     master,
		MaxFreezeSlots: maxFreezeSlots,
	}
}

func (d *DelegateToSequencerLock) Source() string {
	return fmt.Sprintf(delegateToSequencerLockTemplate, d.Target.Source(), d.MasterLock.Source(), d.MaxFreezeSlots)
}

func (d *DelegateToSequencerLock) String() string {
	return fmt.Sprintf(delegateToSequencerLockTemplateHR, d.Target.String(), d.MasterLock.String(), d.MaxFreezeSlots)
}

func (d *DelegateToSequencerLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegateToSequencerLock) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.Target, d.MasterLock})
}

func DelegateToSequencerLockFromBytes(data []byte) (*DelegateToSequencerLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 3)
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
	a2, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: wrong max coverage lock slots: %v", err)
	}
	if a2 >= math.MaxUint16 {
		return nil, fmt.Errorf("DelegateToSequencerLockFromBytes: wrong max coverage lock slots")
	}
	ret.MaxFreezeSlots = uint16(a2)
	return ret, nil
}

func (d *DelegateToSequencerLock) Name() string {
	return DelegateToSequencerLockName
}

func (d *DelegateToSequencerLock) Master() Accountable {
	return d.MasterLock
}

var (
	_safeRevocationSlots atomic.Uint64
)

func DelegationSafeRevocationSlots() int {
	if ret := _safeRevocationSlots.Load(); ret != 0 {
		return int(ret)
	}
	_precalcDelegationConstants()
	return DelegationSafeRevocationSlots()
}

func _precalcDelegationConstants() {
	res, err := L().EvalFromSource(nil, "constDelegationSafeRevocationSlots")
	util.AssertNoError(err)
	_safeRevocationSlots.Store(binary.BigEndian.Uint64(res))
}

func registerDelegateToSequencerLock(lib *Library) {
	lib.mustRegisterConstraint(DelegateToSequencerLockName, 3, func(data []byte) (Constraint, error) {
		return DelegateToSequencerLockFromBytes(data)
	}, initTestDelegateToSequencerConstraint)
	lib.mustRegisterLock(DelegateToSequencerLockName, func(bytes []byte) (Lock, error) {
		ret, err := DelegateToSequencerLockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
	lib.mustRegisterConstraint(delegateToSequencerLockStateName, 2, func(data []byte) (Constraint, error) {
		return DelegateToSequencerLockStateFromBytes(data)
	}, initTestDelegateToSequencerLockState)
}

func initTestDelegateToSequencerConstraint() {
	target := ChainLockFromChainID(base.RandomChainID())
	master := AddressED25519Random()
	example := NewDelegateToSequencerLock(target, master, 3000)

	exampleBack, err := DelegateToSequencerLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.MaxFreezeSlots == 3000, "DelegateToSequencerLockFromBytes: wrong back")
	util.Assertf(exampleBack.MaxFreezeSlots == example.MaxFreezeSlots, "DelegateToSequencerLockFromBytes: wrong back")

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

func DelegateToSequencerLockStateFromBytes(data []byte) (DelegateToSequencerLockState, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return DelegateToSequencerLockState{}, fmt.Errorf("DelegateToSequencerLockStateFromBytes: %w", err)
	}
	if sym != delegateToSequencerLockStateName {
		return DelegateToSequencerLockState{}, fmt.Errorf("DelegateToSequencerLockStateFromBytes: not a DelegateToSequencerLockState")
	}
	fr, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return DelegateToSequencerLockState{}, fmt.Errorf("DelegateToSequencerLockStateFromBytes: wrong argument 0: %w", err)
	}
	if fr >= base.MaxSlot {
		return DelegateToSequencerLockState{}, fmt.Errorf("DelegateToSequencerLockStateFromBytes: wrong argument 0")
	}
	return DelegateToSequencerLockState{
		UnfreezeEpoch: fr,
		Revoked:       !easyfl_util.IsZero(easyfl.StripDataPrefix(args[1])),
	}, nil
}

func (d DelegateToSequencerLockState) Source() string {
	r := "0x"
	if d.Revoked {
		r = "0xff"
	}
	return fmt.Sprintf(delegateToSequencerLockStateTemplate, d.UnfreezeEpoch, r)
}

func (d DelegateToSequencerLockState) String() string {
	return fmt.Sprintf(delegateToSequencerLockStateTemplateHR, d.UnfreezeEpoch, d.Revoked)
}

func (d DelegateToSequencerLockState) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegateToSequencerLockState) Name() string {
	return delegateToSequencerLockStateName
}

func initTestDelegateToSequencerLockState() {
	dlz := DelegateToSequencerLockState{1337, true}

	dlzBack, err := DelegateToSequencerLockStateFromBytes(dlz.Bytes())
	util.AssertNoError(err)
	util.Assertf(dlzBack.UnfreezeEpoch == 1337, "DelegateToSequencerLockState: inconsistency 1")
	util.Assertf(dlzBack.Revoked, "DelegateToSequencerLockState: inconsistency 2")
	util.Assertf(dlz == dlzBack, "DelegateToSequencerLockState: inconsistency 3")

	dlz = DelegateToSequencerLockState{222, false}

	dlzBack, err = DelegateToSequencerLockStateFromBytes(dlz.Bytes())
	util.AssertNoError(err)
	util.Assertf(dlzBack.UnfreezeEpoch == 222, "DelegateToSequencerLockState: inconsistency 1")
	util.Assertf(!dlzBack.Revoked, "DelegateToSequencerLockState: inconsistency 4")
	util.Assertf(dlz == dlzBack, "DelegateToSequencerLockState: inconsistency 5")
}

type MakeDelegateToSequencerOutputParams struct {
	Amount         uint64
	Master         Accountable
	Target         ChainLock
	MaxFreezeSlots uint16
	Revoked        bool
	UnfreezeEpoch  uint64
}

func MakeDelegateToSequencerOutput(par MakeDelegateToSequencerOutputParams) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithAmount(par.Amount)
		o.WithLock(NewDelegateToSequencerLock(par.Target, par.Master, par.MaxFreezeSlots))
		o.MustPushConstraint(NewChainOrigin().Bytes())
		o.MustPushConstraint(DelegateToSequencerLockState{
			UnfreezeEpoch: par.UnfreezeEpoch,
			Revoked:       par.Revoked,
		}.Bytes())
	})
}

const delegateToSequencerLockSource = `
func constDelegationSafeRevocationSlots  : u64/30

func _isDelegationOrigin : isOriginChainData(selfChainData(2))
func _selfChainID : chainID(selfChainData(2))

// $0 index of the chain constraint in the consumed output
func pathToSuccessorOutput : concat(pathToProducedOutputs, byte(selfSiblingUnlockParams($0), 0))

// $0 index of the chain constraint on the predecessor (consumed output)
func successorConstraint : atPath(concat(pathToSuccessorOutput($0), lockConstraintIndex))

// $0 unfreeze slot
// $1 revoked
// placeholder for args. Always returns true
func delegateToSequencerLockState : concat($1,1)

func _unfreezeSlot : uint8Bytes(parseInlineDataArgument(selfSiblingConstraint(3),#delegateToSequencerLockState, 0))
func _isRevoked : parseInlineDataArgument(selfSiblingConstraint(3),#delegateToSequencerLockState, 1)

// checks validity of the composition of the produced constraint 
// $0 max freeze slots
func _validDelegationProduced :
and(
  selfIsProducedOutput,
  enforceMinimumStorageDeposit,
  require(
	 equal(selfNumConstraints, u64/4), 
	 !!!delegation_must_have_exactly_4_constraints
  ), // prevent injection attacks
  require(
	 equal(parsePrefixBytecode(selfSiblingConstraint(2)), #chain), 
	 !!!#chain_is_expected_at_index_2
  ),
  require(
	 equal(parsePrefixBytecode(selfSiblingConstraint(3)), #delegateToSequencerLockState), 
	 !!!#delegateToSequencerLockState_is_expected_at_index_3
  ),
  require(
	 or(not(_isDelegationOrigin), and(not(_isRevoked), isZero(_unfreezeSlot))), 
	 !!!wrong_start_parameters
  ),
  require(
     lessOrEqualThan(len($0), u64/2),
     !!!too_long_max_freeze_slots_parameter
  ),
)

func _amountAtSuccessor : amountValueByOutputPath(concat(pathToProducedOutputs, byte(selfSiblingUnlockParams(2), 0)))

func _insideSafeRevocationWindow : and(
    not(_isDelegationOrigin),
	lessOrEqualThan(uint8Bytes(_unfreezeSlot), uint8Bytes(txSlot)),
    lessThan(uint8Bytes(txSlot), add(_unfreezeSlot, constDelegationSafeRevocationSlots))
)

// $0 target chain lock
// $1 master lock
// $2 max freeze slots
func _validDelegationConsumed : and(
   selfIsConsumedOutput,
   or(
        // master unlocked
      and(
         $1,
         require( or(_isRevoked, lessOrEqualThan(uint8Bytes(_unfreezeSlot), uint8Bytes(txSlot))), !!!master_can_only_unlock_revoked_or_unfrozen),
      ),
        // or target unlocked with conditions
      and(
              // if it is revoked, only master can unlock it
         require(not(_isRevoked), !!!delegation_target_should_not_be_revoked),
         require(not(_insideSafeRevocationWindow), !!!delegation_target_should_not_be_unlocked_inside_safe_revocation_window),
			  // target lock must be unlocked
		 require($0, !!!delegation_target_must_be_unlocked),  
			  // amount should not decrease
		 require(lessOrEqualThan(selfAmountValue, _amountAtSuccessor), !!!amount_should_not_decrease),
			  // delegation lock must be immutable
		 require(equal(successorConstraint(2), selfSiblingConstraint(lockConstraintIndex)), !!!delegation_lock_must_be_immutable),
      )
   )
)

// Delegation lock output. Immutable 
// $0 target chain lock
// $1 master lock
// $2 max freeze slots
func delegateToSequencerLock: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
    or(
       _validDelegationProduced($2),
       _validDelegationConsumed($0,$1)
    )
)

`
