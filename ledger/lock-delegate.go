package ledger

import "fmt"

// DelegationLock is a basic delegation lock which is:
// - unlockable by owner any slot
// = unlockable by delegation target on odd slots (slot mod 2 == 0) with additional constraints
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

// $0 chain constraint index
// $1 target lock
func _enforceDelegationTargetConstraints : concat($0, $1)

// $0 chain constraint index
// $1 target lock
// $2 owner lock
func delegationLock: if(
   isZero(mod(txTimeSlot,2)),
   _enforceDelegationTargetConstraints($0, $1),
   $2
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
	ret := &DelegationLock{}
	if len(args[0]) != 1 || args[0][0] == 255 {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong chain constraint index")
	}
	ret.ChainConstraintIndex = args[0][0]

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
	})
}
