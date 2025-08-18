package ledger

import (
	"bytes"
	"fmt"
	"math"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type (
	DelegateLock struct {
		Target                      ChainLock
		MasterLock                  Accountable
		MaxFrozenSlots              uint16
		MinInflationAdvancePerEpoch uint64
	}
	DelegateLockState struct {
		LastFrozenEpoch uint32
		Revoked         bool
	}
)

const (
	DelegateLockName       = "delegateLock"
	DelegateLockTemplate   = DelegateLockName + "(%s, %s, z16/%d, z64/%d)"
	DelegateLockTemplateHR = DelegateLockName + "(target=%s, master=%s, maxFreezeSlots=%d, inflAdvancePerEpoch=%s)"

	DelegateLockStateName       = "delegateLockState"
	DelegateLockStateTemplate   = DelegateLockStateName + "(z32/%d, %s)"
	DelegateLockStateTemplateHR = DelegateLockStateName + "(frozenUntilEpoch=%d, revoked=%v)"
)

//------------ DelegateLock

func NewDelegateLock(target ChainLock, master Accountable, maxFreezeSlots uint16, minInflationAdvancePerEpoch uint64) *DelegateLock {
	return &DelegateLock{
		Target:                      target,
		MasterLock:                  master,
		MaxFrozenSlots:              maxFreezeSlots,
		MinInflationAdvancePerEpoch: minInflationAdvancePerEpoch,
	}
}

func (d *DelegateLock) Source() string {
	return fmt.Sprintf(DelegateLockTemplate, d.Target.Source(), d.MasterLock.Source(), d.MaxFrozenSlots, d.MinInflationAdvancePerEpoch)
}

func (d *DelegateLock) String() string {
	return fmt.Sprintf(DelegateLockTemplateHR, d.Target.String(), d.MasterLock.String(), d.MaxFrozenSlots, util.Th(d.MinInflationAdvancePerEpoch))
}

func (d *DelegateLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegateLock) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.Target, d.MasterLock})
}

func Delegate2LockFromBytes(data []byte) (*DelegateLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 4)
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}
	if sym != DelegateLockName {
		return nil, fmt.Errorf("Delegate2LockFromBytes: not a DelegateLock")
	}
	// chain constraint index
	ret := &DelegateLock{}

	// target lock
	ret.Target, err = ChainLockFromBytes(args[0])
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}
	// master lock
	ret.MasterLock, err = AccountableFromBytes(args[1])
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}

	// max coverage lock slots
	a2, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: wrong max coverage lock slots: %v", err)
	}
	if a2 >= math.MaxUint16 {
		return nil, fmt.Errorf("Delegate2LockFromBytes: wrong max coverage lock slots")
	}
	ret.MaxFrozenSlots = uint16(a2)

	// minimum inflation advance
	ret.MinInflationAdvancePerEpoch, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: wrong inflation advance per epoch: %v", err)
	}

	return ret, nil
}

func (d *DelegateLock) Name() string {
	return DelegateLockName
}

func (d *DelegateLock) Master() Accountable {
	return d.MasterLock
}

func registerDelegateLock(lib *Library) {
	lib.mustRegisterConstraint(DelegateLockName, 4, func(data []byte) (Constraint, error) {
		return Delegate2LockFromBytes(data)
	}, initTestDelegateConstraint)
	lib.mustRegisterLock(DelegateLockName, func(bytes []byte) (Lock, error) {
		ret, err := Delegate2LockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
	lib.mustRegisterConstraint(DelegateLockStateName, 2, func(data []byte) (Constraint, error) {
		return DelegateLockStateFromBytes(data)
	}, initTestDelegate2LockState)
}

func initTestDelegateConstraint() {
	target := ChainLockFromChainID(base.RandomChainID())
	master := AddressED25519Random()
	example := NewDelegateLock(target, master, 3000, 10)

	exampleBack, err := Delegate2LockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.MaxFrozenSlots == 3000, "Delegate2LockFromBytes: wrong back 1")
	util.Assertf(exampleBack.MaxFrozenSlots == example.MaxFrozenSlots, "Delegate2LockFromBytes: wrong back 2")
	util.Assertf(exampleBack.MinInflationAdvancePerEpoch == example.MinInflationAdvancePerEpoch, "Delegate2LockFromBytes: wrong back 3")
	util.Assertf(example.MinInflationAdvancePerEpoch == 10, "Delegate2LockFromBytes: wrong back 4")

	util.Assertf(EqualConstraints(example, exampleBack), "inconsistency 1 "+DelegateLockName)
	exampleBack2, err := LockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(example, exampleBack2), "inconsistency 2 "+DelegateLockName)

	pref1, err := L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)

	pref2, err := L().EvalFromSource(nil, "#"+DelegateLockName)
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(pref1, pref2), "bytes.Equal(pref1, pref2)")
	util.Assertf(example.Source() == exampleBack.Source(), "example.Source()==exampleBack.Source()")
}

//--------------------------- delegationLockFreeze

func DelegateLockStateFromBytes(data []byte) (DelegateLockState, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: %w", err)
	}
	if sym != DelegateLockStateName {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: not a DelegateLockState")
	}
	fr, err := easyfl_util.Uint32FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: wrong argument 0: %w", err)
	}
	return DelegateLockState{
		LastFrozenEpoch: fr,
		Revoked:         !easyfl_util.IsZero(easyfl.StripDataPrefix(args[1])),
	}, nil
}

func (d DelegateLockState) Source() string {
	r := "0x"
	if d.Revoked {
		r = "0xff"
	}
	return fmt.Sprintf(DelegateLockStateTemplate, d.LastFrozenEpoch, r)
}

func (d DelegateLockState) String() string {
	return fmt.Sprintf(DelegateLockStateTemplateHR, d.LastFrozenEpoch, d.Revoked)
}

func (d DelegateLockState) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegateLockState) Name() string {
	return DelegateLockStateName
}

func initTestDelegate2LockState() {
	dlz := DelegateLockState{3001, true}

	dlzBack, err := DelegateLockStateFromBytes(dlz.Bytes())
	util.AssertNoError(err)
	util.Assertf(dlzBack.LastFrozenEpoch == 3001, "DelegateLockState: inconsistency 1")
	util.Assertf(dlzBack.Revoked, "DelegateLockState: inconsistency 2")
	util.Assertf(dlz == dlzBack, "DelegateLockState: inconsistency 3")
}

const delegateLock2Source = `
func constDelegationSafeRevocationSlots  : 30
func constDelegationEpochSlots : u32/512
func constDelegationMaxFrozenEpochs : 4

// $0 target chain ChainID
func delegationEpochOffset : mod( slice($0, 0, 3), constDelegationEpochSlots)

// $0 target chain ChainID
// $1 epoch
func firstSlotInDelegationEpoch :
if(
   isZero($1),
   u64/0,
   sub(mul($1, constDelegationEpochSlots), delegationEpochOffset($0))
)

// $0 target chain ChainID
// $1 slot
func delegationEpochFromSlot :
div(
   add($1, delegationEpochOffset($0)),
   constDelegationEpochSlots
)

func _selfChainID : parseInlineDataArgument(selfSiblingConstraint(2), #chain, 0)
func _isDelegationOrigin : isChainOriginID(_selfChainID)

// $0 index of the constraint on the successor output
func successorConstraint : atPath(concat(pathToProducedOutputs, byte(selfSiblingUnlockParams(2),0), $0))

func _predecessorLastFrozenEpoch : parseInlineDataArgument(consumedConstraintByIndex(selfChainPredInputIndex(2), 3),selfBytecodePrefix, 0)
func _predecessorTokenBalance : amountAt(consumedConstraintByIndex(selfChainPredInputIndex(2), 0), 0)

// $0 last frozen epoch
// $1 revoked
// mutable part of the delegation output
func delegateLockState : 
or(
   // not checked in the consumed context
   not(selfIsProducedOutput),
   // 'produced' context
   require(
      or( not($1), equalUint($0, _predecessorLastFrozenEpoch) ),
      !!!revocation_cant_mutate_frozen_epochs
   )
)

// self id delegation output

func _selfTarget : parseArgumentBytecode(self,selfBytecodePrefix,0)
func _selfTargetChainID : parseInlineDataArgumentAnyPrefix(_selfTarget,0)
func _selfDelegationEpochOffset : delegationEpochOffset(_selfTargetChainID)
func _selfLastFrozenEpoch : uint8Bytes(parseInlineDataArgument(selfSiblingConstraint(3),#delegateLockState, 0))
func _selfIsRevoked : parseInlineDataArgument(selfSiblingConstraint(3),#delegateLockState, 1)
func _selfEpoch : delegationEpochFromSlot(_selfTargetChainID, txSlot)

// $0 _selfLastFrozenEpoch
// $1 _selfEpoch
func __selfFrozenEpochs : if( lessThanUint($0, $1), u64/0, add(sub($0, $1),1) ) 
func _selfFrozenEpochs : __selfFrozenEpochs(_selfLastFrozenEpoch, _selfEpoch)

// $0 - _selfLastFrozenEpoch
func __selfIsNotFrozen : or( isZero($0), lessThan( $0, delegationEpochFromSlot(_selfTargetChainID,txSlot) ) )

func _selfIsNotFrozen : __selfIsNotFrozen(_selfLastFrozenEpoch)

// $0 output slot
func _coveredSlotsInCurrentEpoch :
sub(
   constDelegationEpochSlots,
   mod(
      add($0, _selfDelegationEpochOffset),
      constDelegationEpochSlots 
   )
)

// $0 slot of the output
// $1 last frozen epoch
func _frozenSlots : 
if(
   isZero($1),
   u64/0,
   sub( firstSlotInDelegationEpoch(_selfTargetChainID, add($1,1)), $0 )
)

// $0 slot of the output (either consumed or produced context)
func _selfFrozenSlots : _frozenSlots($0, _selfLastFrozenEpoch)

// $0 slot of the output
func _selfUnfreezeSlot : add($0, _frozenSlots($0, _selfLastFrozenEpoch))

func _equalTo1Of2 : or(equal($0,$1), equal($0,$2))

// $0 minimum advance inflation upon freeze per full epoch 
// returns minimum required amount of inflation advance
func _calcMinimumAdvanceForSuccessor : div(mul(_frozenSlots(txSlot, _selfLastFrozenEpoch),$0), constDelegationEpochSlots)

// $0 max freeze slots
// $1 minimum advance inflation upon freeze per full epoch 
func _validLimits :
and(
    require(
       lessOrEqualThan(len($0), u64/2),
       !!!max_freeze_slots_must_be_max_2_bytes 
    ),
    require(
       lessOrEqualThan(uint8Bytes(_selfFrozenSlots(txSlot)), uint8Bytes($0)),
       !!!frozen_slots_cannot_exceed_maximum_set_by_delegator
    ),
    require(
       lessOrEqualThan(uint8Bytes(_selfFrozenEpochs), uint8Bytes(constDelegationMaxFrozenEpochs)),
       !!!frozen_epochs_cannot_exceed_constDelegationMaxFrozenEpochs
    ),
    require(
       or(_isDelegationOrigin, lessOrEqualThan(_calcMinimumAdvanceForSuccessor($1), sub(selfTokenBalanceValue, _predecessorTokenBalance))),
       !!!not_enough_inflation_advance
    )
)

func _validBase :
and(
    require(
	   equal(selfNumConstraints, u64/4), 
	   !!!delegation_must_have_exactly_4_constraints
    ), // to prevent injection attacks
    require( 
        _equalTo1Of2(parsePrefixBytecode(_selfTarget), #c, #chainLock),
        !!!delegation_target_must_by_chainLock
    ),
    require(
	   equal(parsePrefixBytecode(selfSiblingConstraint(2)), #chain), 
	   !!!#chain_is_expected_at_index_2
    ),
    require(
	   equal(parsePrefixBytecode(selfSiblingConstraint(3)), #delegateLockState), 
	   !!!#delegateLockState_is_expected_at_index_3
    ),
    require(
	   or(not(_isDelegationOrigin), and(not(_selfIsRevoked), isZero(_selfLastFrozenEpoch))), 
	   !!!wrong_delegation_origin_parameters
    )
)

// checks validity of the composition of the produced constraint 
// $0 max freeze slots
// $1 minimum advance inflation upon freeze per full epoch 
func _validDelegationProduced :
and(
    selfIsProducedOutput,
    _validBase,
    _validLimits($0,$1)
)

// $0 master lock
// (consumed context)
func _masterUnlocked : and( $0, require(_selfIsNotFrozen, !!!master_can't_unlock_frozen_delegation_output) )

func _amountOnSuccessor : tokenBalanceByOutputPath(concat(pathToProducedOutputs, byte(selfSiblingUnlockParams(2), 0)))

// $0 unfreezeSlot
func _insideSafeRevocationWindow : and(
    not(_isDelegationOrigin),
	lessOrEqualThan(uint8Bytes($0), uint8Bytes(txSlot)),
    lessThan(uint8Bytes(txSlot), add($0, constDelegationSafeRevocationSlots))
)

func _consumedUnfreezeSlot : _selfUnfreezeSlot( timeSlotOfInputByIndex( selfOutputIndex ) )

func _successorIsRevoked : parseInlineDataArgument(successorConstraint(3),#delegateLockState,1)

// $0 target lock
// 'consumed' context
func _targetUnlocked :
and(
	  // if it is revoked, only master can unlock it
   require(not(_selfIsRevoked), !!!revoked_delegation_cannot_be_unlocked_by_the_target),
   require(not(_insideSafeRevocationWindow(_consumedUnfreezeSlot)), !!!delegation_target_should_not_be_unlocked_inside_safe_revocation_window),
   require( or( _successorIsRevoked, _selfIsNotFrozen ), !!!frozen_delegation_can_be_unlocked_by_the_target_only_for_revocation),
	  // target lock must be unlocked
   require($0, !!!delegation_target_chain_must_be_unlocked),  
	  // amount should not decrease
   require(lessOrEqualThan(selfTokenBalanceValue, _amountOnSuccessor), !!!delegated_amount_should_not_decrease),
	  // delegation lock must be immutable
   require(equal(successorConstraint(1), selfSiblingConstraint(lockConstraintIndex)), !!!delegation_lock_must_be_immutable)
)


// $0 target chain lock
// $1 master lock
func _validDelegationConsumed : and(
   selfIsConsumedOutput,
   or(
      _masterUnlocked($1),
      _targetUnlocked($0)
   )
)

// Delegation lock output. Immutable 
// $0 target chain lock
// $1 master lock
// $2 max freeze slots
// $3 minimum advance inflation upon freeze per full epoch. In order to freeze coverage, target just provide in advance inflation  
func delegateLock: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_index_1),
    or(
       _validDelegationProduced($2, $3),
       _validDelegationConsumed($0,$1)
    )
)

// $0 delegation chain ID
// Checks unlock conditions. Conditions are satisfied when unlock data is one bte with the number of
// produced output that is delegation output with the given delegation chain ID and it is revoked
// 
// This constraint script is attached to the sequencer command. 
// Its purpose is to enforce real revocation of the delegation by the sequencer  
func ensureRevocation :
or(
   selfIsProducedOutput,
   and(
      selfIsConsumedOutput,
      require(
		and(
		   equal(
			  parseArgumentBytecode(producedConstraintByIndex(concat(selfUnlockParameters,2)),#chain,0), 
			  $0
		   ),
		   parseInlineDataArgument(producedConstraintByIndex(concat(selfUnlockParameters,3)),#delegateLockState,1)
		),
        !!!delegation_output_is_not_revoked_as_expected
      )
   )
)

`
