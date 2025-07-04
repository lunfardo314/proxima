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
	"github.com/lunfardo314/proxima/util/lines"
)

type (
	DelegateLock2 struct {
		Target         ChainLock
		MasterLock     Accountable
		MaxFreezeSlots uint16
	}
	DelegateLock2State struct {
		UnfreezeSlot base.Slot
		Revoked      bool
	}

	Delegate2Output struct {
		OutputWithChainID
		DelegateLock2
		DelegateLock2State
	}
)

const (
	Delegate2LockName       = "delegateLock2"
	Delegate2LockTemplate   = Delegate2LockName + "(%s, %s, z16/%d)"
	Delegate2LockTemplateHR = Delegate2LockName + "(target=%s, master=%s, maxFreezeSlots=%d)"

	Delegate2LockStateName       = "delegateLock2State"
	Delegate2LockStateTemplate   = Delegate2LockStateName + "(z32/%d, %s)"
	Delegate2LockStateTemplateHR = Delegate2LockStateName + "(unfreezeSlot=%d, revoked=%v)"
)

//------------ DelegateLock2

func NewDelegate2Lock(target ChainLock, master Accountable, maxFreezeSlots uint16) *DelegateLock2 {
	return &DelegateLock2{
		Target:         target,
		MasterLock:     master,
		MaxFreezeSlots: maxFreezeSlots,
	}
}

func (d *DelegateLock2) Source() string {
	return fmt.Sprintf(Delegate2LockTemplate, d.Target.Source(), d.MasterLock.Source(), d.MaxFreezeSlots)
}

func (d *DelegateLock2) String() string {
	return fmt.Sprintf(Delegate2LockTemplateHR, d.Target.String(), d.MasterLock.String(), d.MaxFreezeSlots)
}

func (d *DelegateLock2) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegateLock2) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.Target, d.MasterLock})
}

func Delegate2LockFromBytes(data []byte) (*DelegateLock2, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 3)
	if err != nil {
		return nil, fmt.Errorf("Delegate2LockFromBytes: %w", err)
	}
	if sym != Delegate2LockName {
		return nil, fmt.Errorf("Delegate2LockFromBytes: not a DelegateLock2")
	}
	// chain constraint index
	ret := &DelegateLock2{}

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
	ret.MaxFreezeSlots = uint16(a2)
	return ret, nil
}

func (d *DelegateLock2) Name() string {
	return Delegate2LockName
}

func (d *DelegateLock2) Master() Accountable {
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

func registerDelegate2Lock(lib *Library) {
	lib.mustRegisterConstraint(Delegate2LockName, 3, func(data []byte) (Constraint, error) {
		return Delegate2LockFromBytes(data)
	}, initTestDelegate2Constraint)
	lib.mustRegisterLock(Delegate2LockName, func(bytes []byte) (Lock, error) {
		ret, err := Delegate2LockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
	lib.mustRegisterConstraint(Delegate2LockStateName, 2, func(data []byte) (Constraint, error) {
		return Delegate2LockStateFromBytes(data)
	}, initTestDelegate2LockState)
}

func initTestDelegate2Constraint() {
	target := ChainLockFromChainID(base.RandomChainID())
	master := AddressED25519Random()
	example := NewDelegate2Lock(target, master, 3000)

	exampleBack, err := Delegate2LockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(example.MaxFreezeSlots == 3000, "Delegate2LockFromBytes: wrong back")
	util.Assertf(exampleBack.MaxFreezeSlots == example.MaxFreezeSlots, "Delegate2LockFromBytes: wrong back")

	util.Assertf(EqualConstraints(example, exampleBack), "inconsistency 1 "+Delegate2LockName)
	exampleBack2, err := LockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(example, exampleBack2), "inconsistency 2 "+Delegate2LockName)

	pref1, err := L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)

	pref2, err := L().EvalFromSource(nil, "#"+Delegate2LockName)
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(pref1, pref2), "bytes.Equal(pref1, pref2)")
	util.Assertf(example.Source() == exampleBack.Source(), "example.Source()==exampleBack.Source()")
}

//--------------------------- delegationLockFreeze

func Delegate2LockStateFromBytes(data []byte) (DelegateLock2State, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return DelegateLock2State{}, fmt.Errorf("Delegate2LockStateFromBytes: %w", err)
	}
	if sym != Delegate2LockStateName {
		return DelegateLock2State{}, fmt.Errorf("Delegate2LockStateFromBytes: not a DelegateLock2State")
	}
	fr, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return DelegateLock2State{}, fmt.Errorf("Delegate2LockStateFromBytes: wrong argument 0: %w", err)
	}
	if fr >= base.MaxSlot {
		return DelegateLock2State{}, fmt.Errorf("Delegate2LockStateFromBytes: wrong argument 0")
	}
	return DelegateLock2State{
		UnfreezeSlot: base.Slot(fr),
		Revoked:      !easyfl_util.IsZero(easyfl.StripDataPrefix(args[1])),
	}, nil
}

func (d DelegateLock2State) Source() string {
	r := "0x"
	if d.Revoked {
		r = "0xff"
	}
	return fmt.Sprintf(Delegate2LockStateTemplate, d.UnfreezeSlot, r)
}

func (d DelegateLock2State) String() string {
	return fmt.Sprintf(Delegate2LockStateTemplateHR, d.UnfreezeSlot, d.Revoked)
}

func (d DelegateLock2State) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d DelegateLock2State) Name() string {
	return Delegate2LockStateName
}

func initTestDelegate2LockState() {
	dlz := DelegateLock2State{1337, true}

	dlzBack, err := Delegate2LockStateFromBytes(dlz.Bytes())
	util.AssertNoError(err)
	util.Assertf(dlzBack.UnfreezeSlot == 1337, "DelegateLock2State: inconsistency 1")
	util.Assertf(dlzBack.Revoked, "DelegateLock2State: inconsistency 2")
	util.Assertf(dlz == dlzBack, "DelegateLock2State: inconsistency 3")

	dlz = DelegateLock2State{222, false}

	dlzBack, err = Delegate2LockStateFromBytes(dlz.Bytes())
	util.AssertNoError(err)
	util.Assertf(dlzBack.UnfreezeSlot == 222, "DelegateLock2State: inconsistency 1")
	util.Assertf(!dlzBack.Revoked, "DelegateLock2State: inconsistency 4")
	util.Assertf(dlz == dlzBack, "DelegateLock2State: inconsistency 5")
}

type MakeDelegate2OutputParams struct {
	Amount         uint64
	Master         Accountable
	Target         ChainLock
	MaxFreezeSlots uint16
	StartSlot      base.Slot
}

func MakeDelegate2InitOutput(par MakeDelegate2OutputParams) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithAmount(par.Amount)
		o.WithLock(NewDelegate2Lock(par.Target, par.Master, par.MaxFreezeSlots))
		o.MustPushConstraint(NewChainOrigin(par.StartSlot, par.Amount).Bytes())
		o.MustPushConstraint(DelegateLock2State{}.Bytes())
	})
}

func AsDelegate2Output(o *OutputWithChainID) (ret Delegate2Output, err error) {
	ret.OutputWithChainID = *o
	lock := o.Output.Lock()
	if lock.Name() != Delegate2LockName {
		err = fmt.Errorf("AsDelegate2Output: not a DelegationToSequencerLock")
		return
	}
	dLock, ok := lock.(*DelegateLock2)
	util.Assertf(ok, "AsDelegate2Output: inconsistency")
	ret.DelegateLock2 = *dLock

	if data, err := o.Output.ConstraintAt(3); err == nil {
		ret.DelegateLock2State, err = Delegate2LockStateFromBytes(data)
	}
	return
}

// SafeRevocationSlots return slots from-to (inclusive) when target cannot consume the delegation output
// (0, 0) means it is revoked, i.e., it cannot be consumed by the target
func (o *Delegate2Output) SafeRevocationSlots() (from, to base.Slot) {
	if o.Revoked {
		return
	}
	return base.Slot(o.UnfreezeSlot), base.Slot(o.UnfreezeSlot) + base.Slot(DelegationSafeRevocationSlots()) - 1
}

func (o *Delegate2Output) LinesSource(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("---- delegation output ----")
	ret.Append(o.OutputWithChainID.Lines("   "))
	ret.Add("Master: %s", o.MasterLock.Source())
	ret.Add("Target: %s", o.Target.Source())
	ret.Add("MaxFreezeSlots: %d", o.MaxFreezeSlots)
	ret.Add("Unfreeze slot: %d", o.UnfreezeSlot)
	revStr := "all (permanently revoked by the master)"
	f, t := o.SafeRevocationSlots()
	util.Assertf(f <= t, "f<=t")
	if f != 0 || t != 0 {
		revStr = fmt.Sprintf("from %d to %d (inclusive)", f, t)
	}
	ret.Add("Safe revocation slots: %s", revStr)
	return ret
}

const delegateLock2Source = `
func constDelegationSafeRevocationSlots  : u64/30

func _selfChainID : parseInlineDataArgument(selfSiblingConstraint(2), #chain, 0)
func _isDelegationOrigin : isChainOriginID(_selfChainID)

// $0 index of the chain constraint in the consumed output
func pathToSuccessorOutput : concat(pathToProducedOutputs, byte(selfSiblingUnlockParams($0), 0))

// $0 index of the chain constraint on the predecessor (consumed output)
func successorConstraint : atPath(concat(pathToSuccessorOutput($0), lockConstraintIndex))

// $0 unfreeze slot
// $1 revoked
// placeholder for args. Always returns true
func delegateLock2State : concat($1,1)

func _unfreezeSlot : uint8Bytes(parseInlineDataArgument(selfSiblingConstraint(3),#delegateLock2State, 0))
func _isRevoked : parseInlineDataArgument(selfSiblingConstraint(3),#delegateLock2State, 1)

func _equalTo1Of2 : or(equal($0,$1), equal($0,$2))

// checks validity of the composition of the produced constraint 
// $0 max freeze slots
func _validDelegation2Produced :
and(
    selfIsProducedOutput,
    enforceMinimumStorageDeposit,
    require(
	   equal(selfNumConstraints, u64/4), 
	   !!!delegation_must_have_exactly_4_constraints
    ), // to prevent injection attacks
    require( 
        _equalTo1Of2(parsePrefixBytecode(parseArgumentBytecode(self,selfBytecodePrefix,0)), #c, #chainLock),
        !!!delegation_target_must_by_chainLock
    ),
    require(
	   equal(parsePrefixBytecode(selfSiblingConstraint(2)), #chain), 
	   !!!#chain_is_expected_at_index_2
    ),
    require(
	   equal(parsePrefixBytecode(selfSiblingConstraint(3)), #delegateLock2State), 
	   !!!#delegateLock2State_is_expected_at_index_3
    ),
    require(
	   or(not(_isDelegationOrigin), and(not(_isRevoked), isZero(_unfreezeSlot))), 
	   !!!wrong_start_parameters
    ),
    require(
       lessOrEqualThan(len($0), u64/2),
       !!!too_max_freeze_slots_must_be_max_2_bytes 
    ),
    require(
       lessThan(_unfreezeSlot, add(txSlot, $0)),
       !!!unfreeze_slot_cannot_exceed_maximum_set_by_delegator
    )
)

func _amountOnSuccessor : amountValueByOutputPath(concat(pathToProducedOutputs, byte(selfSiblingUnlockParams(2), 0)))

func _insideSafeRevocationWindow : and(
    not(_isDelegationOrigin),
	lessOrEqualThan(uint8Bytes(_unfreezeSlot), uint8Bytes(txSlot)),
    lessThan(uint8Bytes(txSlot), add(_unfreezeSlot, constDelegationSafeRevocationSlots))
)

// $0 target chain lock
// $1 master lock
// $2 max freeze slots
func _validDelegation2Consumed : and(
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
         require(not(_isRevoked), !!!revoked_delegation_cannot_be_unlocked_by_the_target),
         require(not(_insideSafeRevocationWindow), !!!delegation_target_should_not_be_unlocked_inside_safe_revocation_window),
			  // target lock must be unlocked
		 require($0, !!!delegation_target_must_be_unlocked),  
			  // amount should not decrease
		 require(lessOrEqualThan(selfAmountValue, _amountOnSuccessor), !!!delegated_amount_should_not_decrease),
			  // delegation lock must be immutable
		 require(equal(successorConstraint(2), selfSiblingConstraint(lockConstraintIndex)), !!!delegation_lock_must_be_immutable),
      )
   )
)

// Delegation lock output. Immutable 
// $0 target chain lock
// $1 master lock
// $2 max freeze slots
func delegateLock2: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_index_1), 
    or(
       _validDelegation2Produced($2),
       _validDelegation2Consumed($0,$1)
    )
)
`
