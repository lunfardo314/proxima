package ledger

import (
	"bytes"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

// DelegationLock is a basic delegation lock which is:
// - unlockable by owner any slot
// = unlockable by delegation target on even slots (slot mod 2 == 0) with additional constraints
type DelegationLock struct {
	TargetLock Accountable
	OwnerLock  Accountable
	// must point to the sibling chain constraint
	ChainConstraintIndex byte
}

const (
	DelegationLockName     = "delegationLock"
	delegationLockTemplate = DelegationLockName + "(%d, %s, %s)"
)

const delegationLockSource = `

// Enfoces delegation target lock and additional constraints, such as immutable chain 
// transition with non-decreasing amount
// $0 chain constraint index
// $1 target lock
// $2 successor output
func _enforceDelegationTargetConstraintsOnSuccessor : and(
    $1,  // target lock must be unlocked
    require(lessOrEqualThan(selfAmountValue, amountValue($2)), !!!amount_should_not_decrease),
    require(equal(@Array8($2, lockConstraintIndex), selfSiblingConstraint(lockConstraintIndex)), !!!lock_must_be_immutable),
    require(equal(byte(selfUnlockParameters,2), 0), !!!chain_must_be_state_transition)
)
	

// $0 chain constraint index
// $1 target lock
// $2 owner lock
func delegationLock: and(
	mustSize($0,1),
	require(not(equal($0, 0xff)), !!!chain_constraint_index_0xff_is_not_alowed),
    require(equal(parsePrefixBytecode(selfSiblingConstraint($0)), #chain), !!!wrong_chain_constraint_index),
    or(
		and(selfIsProducedOutput, $1, $2),  // check general consistency on produced output
        and(
            selfIsConsumedOutput,
            or(
               and(  // check delegation case on even slots
                   isZero(bitwiseAND(txTimeSlot,u32/1)),   // check if even
                  _enforceDelegationTargetConstraintsOnSuccessor(
                      $0,
                      $1, 
                      producedOutputByIndex(byte(selfUnlockParameters,0))
                  ), // check successor
               ),
               $2 // otherwise check owner's lock
            )
        )
    )
)
`

func NewDelegationLock(owner, target Accountable, chainConstraintIndex byte) *DelegationLock {
	return &DelegationLock{
		TargetLock:           target,
		OwnerLock:            owner,
		ChainConstraintIndex: chainConstraintIndex,
	}
}

func DelegationLockFromBytes(data []byte) (*DelegationLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 3)
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: %w", err)
	}
	if sym != DelegationLockName {
		return nil, fmt.Errorf("DelegationLockFromBytes: not a DelegationLock")
	}
	arg0 := easyfl.StripDataPrefix(args[0])
	ret := &DelegationLock{}
	if len(arg0) != 1 || arg0[0] == 255 {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong chain constraint index")
	}
	ret.ChainConstraintIndex = arg0[0]

	ret.TargetLock, err = AccountableFromBytes(args[1])
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: %w", err)
	}
	ret.OwnerLock, err = AccountableFromBytes(args[2])
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: %w", err)
	}
	return ret, nil
}

func (d *DelegationLock) Source() string {
	return fmt.Sprintf(delegationLockTemplate, d.ChainConstraintIndex, d.TargetLock.Source(), d.OwnerLock.Source())
}

func (d *DelegationLock) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegationLock) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.TargetLock, d.OwnerLock})
}

func (d *DelegationLock) Name() string {
	return DelegationLockName
}

func (d *DelegationLock) String() string {
	return d.Source()
}

func addDelegationLock(lib *Library) {
	lib.extendWithConstraint(DelegationLockName, delegationLockSource, 3, func(data []byte) (Constraint, error) {
		return DelegationLockFromBytes(data)
	}, initTestDelegationConstraint)
}

func initTestDelegationConstraint() {
	a1 := AddressED25519Random()
	a2 := AddressED25519Random()
	example := NewDelegationLock(a1, a2, 1)
	exampleBack, err := DelegationLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(example, exampleBack), "inconsistency "+DelegationLockName)

	pref1, err := L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)

	pref2, err := L().EvalFromSource(nil, "#delegationLock")
	util.AssertNoError(err)
	util.Assertf(bytes.Equal(pref1, pref2), "bytes.Equal(pref1, pref2)")
	util.Assertf(example.Source() == exampleBack.Source(), "example.Source()==exampleBack.Source()")
}
