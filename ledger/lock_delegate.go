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
		MaxInflationMarginTolerance uint16 // in promille, <= 1000
	}
	DelegateLockState struct {
		LastFrozenEpoch uint32
		State           byte
	}

	EnsureRevocation struct {
		base.ChainID
	}
)

const (
	DelegateLockName       = "delegateLock"
	DelegateLockTemplate   = DelegateLockName + "(%s, %s, z16/%d, z16/%d)"
	DelegateLockTemplateHR = DelegateLockName + "(target=%s, master=%s, maxFreezeSlots=%d, maxInflationMarginTolerance=%d%%%%)"

	DelegateLockStateName       = "delegateLockState"
	DelegateLockStateTemplate   = DelegateLockStateName + "(z32/%d, %d)"
	DelegateLockStateTemplateHR = DelegateLockStateName + "(frozenUntilEpoch=%d, state=%s)"

	DelegateLockStateUndef   = byte(0)
	DelegateLockStateFrozen  = byte(1)
	DelegateLockStateRevoked = byte(2)

	EnsureRevocationName       = "ensureRevocation"
	EnsureRevocationTemplate   = EnsureRevocationName + "(0x%s)"
	EnsureRevocationTemplateHR = EnsureRevocationName + "(%s)"
)

//------------ DelegateLock

func NewDelegateLock(target ChainLock, master Accountable, maxFreezeSlots uint16, maxToleratedCostMargin uint16) *DelegateLock {
	return &DelegateLock{
		Target:                      target,
		MasterLock:                  master,
		MaxFrozenSlots:              maxFreezeSlots,
		MaxInflationMarginTolerance: maxToleratedCostMargin,
	}
}

func (d *DelegateLock) Source() string {
	return fmt.Sprintf(DelegateLockTemplate, d.Target.Source(), d.MasterLock.Source(), d.MaxFrozenSlots, d.MaxInflationMarginTolerance)
}

func (d *DelegateLock) String() string {
	return fmt.Sprintf(DelegateLockTemplateHR, d.Target.String(), d.MasterLock.String(), d.MaxFrozenSlots, d.MaxInflationMarginTolerance)
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
	ret.MaxInflationMarginTolerance, err = easyfl_util.Uint16FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: wrong max inflation margin: %v", err)
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
	lib.mustRegisterConstraint(EnsureRevocationName, 1, func(data []byte) (Constraint, error) {
		return EnsureRevocationFromBytes(data)
	}, initTestEnsureRevocation)
}

func initTestDelegateConstraint() {
	target := ChainLockFromChainID(base.RandomChainID())
	master := AddressED25519Random()
	example := NewDelegateLock(target, master, 3000, 10)

	exampleBack, err := Delegate2LockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.MaxFrozenSlots == 3000, "Delegate2LockFromBytes: wrong back 1")
	util.Assertf(exampleBack.MaxFrozenSlots == example.MaxFrozenSlots, "Delegate2LockFromBytes: wrong back 2")
	util.Assertf(exampleBack.MaxInflationMarginTolerance == example.MaxInflationMarginTolerance, "Delegate2LockFromBytes: wrong back 3")
	util.Assertf(example.MaxInflationMarginTolerance == 10, "Delegate2LockFromBytes: wrong back 4")

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

//--------------------------- delegationLockState

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
	state := easyfl.StripDataPrefix(args[1])
	if len(state) != 1 {
		return DelegateLockState{}, fmt.Errorf("DelegateLockStateFromBytes: argument 1 must be one byte")
	}
	return DelegateLockState{
		LastFrozenEpoch: fr,
		State:           state[0],
	}, nil
}

func (d DelegateLockState) Source() string {
	return fmt.Sprintf(DelegateLockStateTemplate, d.LastFrozenEpoch, d.State)
}

func (d DelegateLockState) String() string {
	s := "undef"
	switch d.State {
	case DelegateLockStateFrozen:
		s = "frozen"
	case DelegateLockStateRevoked:
		s = "revoked"
	}
	return fmt.Sprintf(DelegateLockStateTemplateHR, d.LastFrozenEpoch, s)
}

func (d DelegateLockState) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegateLockState) Name() string {
	return DelegateLockStateName
}

func initTestDelegate2LockState() {
	dlz := DelegateLockState{3001, DelegateLockStateFrozen}

	dlzBack, err := DelegateLockStateFromBytes(dlz.Bytes())
	util.AssertNoError(err)
	util.Assertf(dlzBack.LastFrozenEpoch == 3001, "DelegateLockState: inconsistency 1")
	util.Assertf(dlzBack.State == DelegateLockStateFrozen, "DelegateLockState: inconsistency 2")
	util.Assertf(dlz == dlzBack, "DelegateLockState: inconsistency 3")
}

//--------------------------- delegationLockState

func EnsureRevocationFromBytes(data []byte) (*EnsureRevocation, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, fmt.Errorf("EnsureRevocationFromBytes: %w", err)
	}
	if sym != EnsureRevocationName {
		return nil, fmt.Errorf("EnsureRevocationFromBytes: not a EnsureRevocation")
	}
	delegationID, err := base.ChainIDFromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, err
	}
	return &EnsureRevocation{delegationID}, nil
}

func (d *EnsureRevocation) Source() string {
	return fmt.Sprintf(EnsureRevocationTemplate, d.ChainID.StringHex())
}

func (d *EnsureRevocation) String() string {
	return fmt.Sprintf(EnsureRevocationTemplateHR, d.ChainID.String())
}

func (d *EnsureRevocation) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *EnsureRevocation) Name() string {
	return EnsureRevocationName
}

func initTestEnsureRevocation() {
	e := EnsureRevocation{base.RandomChainID()}

	eBack, err := EnsureRevocationFromBytes(e.Bytes())
	util.AssertNoError(err)
	util.Assertf(eBack.ChainID == e.ChainID, "EnsureRevocation: inconsistency")
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
// $1 state. 1 byte-long. 
//  - 0x01 means 'frozen'
//  - 0x02 means 'revoked'. 
//  - The rest combinations means 'neither revoked, not frozen'
// 
// mutable part of the delegation output
func delegateLockState : 
or(
   // not checked in the consumed context
   not(selfIsProducedOutput),
   // 'produced' context
   require(equal(len($1), u64/1), !!!delegateLockState_$1_must_be_1_byte), 
       // TODO why we need this?
   require(
     or( not(equal($1,2)), equalUint($0, _predecessorLastFrozenEpoch) ),
     !!!revocation_cant_mutate_frozen_epochs
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

// self id delegation output - does not depend on consumed/produced context

func _selfTarget : parseArgumentBytecode(self,selfBytecodePrefix,0)
func _selfTargetChainID : parseInlineDataArgumentAnyPrefix(_selfTarget,0)
func _selfDelegationEpochOffset : delegationEpochOffset(_selfTargetChainID)
func _selfLastFrozenEpoch : uint8Bytes(parseInlineDataArgument(selfSiblingConstraint(3),#delegateLockState, 0))
func _selfStatus : parseInlineDataArgument(selfSiblingConstraint(3),#delegateLockState, 1)
func _selfIsFrozen : equal(_selfStatus, 1)
func _selfIsRevoked : equal(_selfStatus, 2)
func _selfIsStatusUndef : and(not(_selfIsRevoked), not(_selfIsFrozen))

func _successorEpoch : delegationEpochFromSlot(_selfTargetChainID, txSlot)
func _successorStatus : parseInlineDataArgument(successorConstraint(3),#delegateLockState, 1)

// $0 _selfLastFrozenEpoch
// $1 _successorEpoch
func __txFrozenEpochs : if( lessThanUint($0, $1), u64/0, add(sub($0, $1),1) ) 
func _txFrozenEpochs : __txFrozenEpochs(_selfLastFrozenEpoch, _successorEpoch)

// $0 - _selfLastFrozenEpoch
func __txIsNotFrozen : or( isZero($0), lessThan( $0, delegationEpochFromSlot(_selfTargetChainID,txSlot) ) )
func _txIsNotFrozen : __txIsNotFrozen(_selfLastFrozenEpoch)

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

// $0 amount
// $1 margin in promille
// = a(1-margin/1000) = (a*(1000-margin))/1000
func _shaveMargin : div( mul($0, sub(u64/1000,$1)), u64/1000 )

// $0 frozen slots in the transaction
// $1 slot of the transaction
// $2 predecessor token balance
// $3 margin to shave in promille
func requiredMinimumInflationAdvance :
	_shaveMargin(
	  mul(
		add($0, u64/1),  // plus 1 slot for the inflation in the current transaction 
		chainInflationOneSlot($1, $2)
	  ),
	  $3
	)


// $0 max tolerated inflation cost margin, the part of the inflation shaved by the sequencer. uint64
// $1 _predecessorTokenBalance
//
// It uses an approximation (linear extrapolation) of the future projected inflation (non-linear)
// At the sequencer side, it must be taken into account that margins are not the same for 
// the delegator and the sequencer. The difference is minor, however
func _requiredMinimumInflationAdvance :
     requiredMinimumInflationAdvance(_selfFrozenSlots(txSlot), txSlot, $1, $0) 

// $0 max tolerated inflation cost margin, the part of the inflation shaved by the sequencer. uint64
// $1 _predecessorTokenBalance
func _validInflationAdvanceProduced :
or(
    not(_selfIsFrozen),
	and(
		require(
		   lessOrEqualThan($0, u64/1000),
		   !!!max_inflation_cost_margin_must_be_in_promille_less_or_equal_than_1000
		),
		require(
		   lessOrEqualThan( _requiredMinimumInflationAdvance($0, $1), sub(selfTokenBalanceValue, $1)),
		   !!!not_enough_inflation_advance
		)
	)
)

// $0 max freeze slots (uint64)
func _validLimitsProduced :
and(
    require(
       lessOrEqualThan(uint8Bytes(_selfFrozenSlots(txSlot)), $0),
       !!!frozen_slots_cannot_exceed_maximum_set_by_delegator
    ),
    require(
       lessOrEqualThan(_txFrozenEpochs, uint8Bytes(constDelegationMaxFrozenEpochs)),
       !!!frozen_epochs_cannot_exceed_constDelegationMaxFrozenEpochs
    ),
    if(
       or(_isDelegationOrigin, _selfIsStatusUndef),
       require( and(isZero(_selfLastFrozenEpoch), equal(_selfStatus, 0)), !!!wrong_delegation_origin_parameters),
       true
    )
)

func _validStructureProduced :
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
    )
)

// checks validity of the composition of the produced constraint 
// $0 max freeze slots uint64
// $1 max inflation margin tolerance, the part of the inflation shaved by the sequencer 
func _validDelegationProduced :
and(
    selfIsProducedOutput,
    _validStructureProduced,
    _validLimitsProduced($0),
    _validInflationAdvanceProduced($1, _predecessorTokenBalance)
)

func _consumedIsMasterUnlocked : equal(byte(selfUnlockParameters,2), 0xff)

// $0 master lock
// (consumed context)
func _masterUnlockedConsumed : 
and( 
    _consumedIsMasterUnlocked, 
    $0, 
    require(_txIsNotFrozen, !!!master_can't_unlock_frozen_delegation_output) 
)

func _amountOnSuccessor : tokenBalanceByOutputPath(concat(pathToProducedOutputs, byte(selfSiblingUnlockParams(2), 0)))

// $0 unfreezeSlot
func _txInsideSafeRevocationWindow : and(
    not(_isDelegationOrigin),
	lessOrEqualThan(uint8Bytes($0), uint8Bytes(txSlot)),
    lessThan(uint8Bytes(txSlot), add($0, constDelegationSafeRevocationSlots))
)

func _consumedUnfreezeSlot : _selfUnfreezeSlot( slotOfInputByIndex( selfOutputIndex ) )

func _successorIsRevoked : parseInlineDataArgument(successorConstraint(3),#delegateLockState,1)

// $0 target lock
// 'consumed' context
func _targetUnlockedConsumed :
and(
   not(_consumedIsMasterUnlocked),
	  // if it is revoked, only master can unlock it
   require(not(_selfIsRevoked), !!!revoked_delegation_cannot_be_unlocked_by_the_target),
   require(not(_txInsideSafeRevocationWindow(_consumedUnfreezeSlot)), !!!delegation_target_should_not_be_unlocked_inside_safe_revocation_window),
   require( or( _successorIsRevoked, _txIsNotFrozen ), !!!frozen_delegation_can_be_unlocked_by_the_target_only_for_revocation),
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
   require(
      equal(len(selfUnlockParameters), u64/3), 
      !!!unlock_parameters_of_the_delegation_lock_must_be_3_bytes_long
   ),
   or(
      _masterUnlockedConsumed($1),
      _targetUnlockedConsumed($0)
   )
)

// Delegation lock output. Immutable 
// $0 target chain lock
// $1 master lock
// $2 max freeze slots
// $3 max tolerated inflation cost margin, the part of inflation sequencer is allowed to shave from the inflation. In promille   
//
// Unlock parameters 3 bytes first 1 or 2 bytes are used to unlock address or chain locks
// Bytes with index 2 must be 0xff for master unlock, otherwise it is target unlock 
func delegateLock: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_index_1),
    or(
       _validDelegationProduced(uint8Bytes($2), uint8Bytes($3)),
       _validDelegationConsumed($0, $1)
    )
)

`
