package ledger

import (
	"bytes"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type DelegationLock2 struct {
	TargetLock Accountable
	OwnerLock  Accountable
	// must point to the sibling chain constraint
	ChainConstraintIndex    byte
	StartSlot               base.Slot
	StartAmount             uint64
	LockedCoverageUntilSlot base.Slot
}

const (
	DelegationLock2Name     = "delegationLock2"
	delegationLock2Template = DelegationLock2Name + "(%d, %s, %s, z32/%d, z64/%d, z32/%d)"
)

func NewDelegationLock2(owner, target Accountable, chainConstraintIndex byte, startSlot base.Slot, startAmount uint64) *DelegationLock2 {
	return &DelegationLock2{
		TargetLock:              target,
		OwnerLock:               owner,
		ChainConstraintIndex:    chainConstraintIndex,
		StartSlot:               startSlot,
		StartAmount:             startAmount,
		LockedCoverageUntilSlot: 0,
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
	arg0 := easyfl.StripDataPrefix(args[0])
	ret := &DelegationLock2{}
	if len(arg0) != 1 || arg0[0] == 255 {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong chain constraint index")
	}
	ret.ChainConstraintIndex = arg0[0]

	ret.TargetLock, err = AccountableFromBytes(args[1])
	if err != nil {
		return nil, fmt.Errorf("DelegationLock2FromBytes: %w", err)
	}
	ret.OwnerLock, err = AccountableFromBytes(args[2])
	if err != nil {
		return nil, fmt.Errorf("DelegationLock2FromBytes: %w", err)
	}

	startSlot64, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3]))
	if err != nil {
		return nil, fmt.Errorf("DelegationLock2FromBytes: %w", err)
	}
	if startSlot64 >= base.MaxSlot {
		return nil, fmt.Errorf("DelegationLock2FromBytes: start slot %d out of range", startSlot64)
	}
	ret.StartSlot = base.Slot(startSlot64)

	ret.StartAmount, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[4]))
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong start amount")
	}
	locCov, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[5]))
	if err != nil {
		return nil, fmt.Errorf("DelegationLockFromBytes: wrong locked until slot data")
	}
	if locCov >= base.MaxSlot {
		return nil, fmt.Errorf("DelegationLockFromBytes: 'locked until slot' is out of range")
	}
	ret.LockedCoverageUntilSlot = base.Slot(locCov)
	return ret, nil
}

func (d *DelegationLock2) Source() string {
	return fmt.Sprintf(delegationLock2Template,
		d.ChainConstraintIndex, d.TargetLock.Source(), d.OwnerLock.Source(), d.StartSlot, d.StartAmount, d.LockedCoverageUntilSlot)
}

func (d *DelegationLock2) String() string {
	return fmt.Sprintf("%s(chainIdx=%d, target=%s, owner=%s, startSlot=%d, startAmount=%s, lockedCoverageUntil=%d)",
		DelegationLock2Name, d.ChainConstraintIndex, d.TargetLock.String(), d.OwnerLock.String(), d.StartSlot, util.Th(d.StartAmount), d.LockedCoverageUntilSlot)
}

func (d *DelegationLock2) Bytes() []byte {
	return mustBinFromSource(d.Source())
}

func (d *DelegationLock2) Accounts() []Accountable {
	return NoDuplicatesAccountables([]Accountable{d.TargetLock, d.OwnerLock})
}

func (d *DelegationLock2) Name() string {
	return DelegationLock2Name
}

func (d *DelegationLock2) Master() Accountable {
	return d.OwnerLock
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
	example := NewDelegationLock2(a1, a2, 1, slotNow, 1337)
	exampleBack, err := DelegationLock2FromBytes(example.Bytes())
	util.AssertNoError(err)
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

const delegationLock2Source = `

// $0 chain constraint index
// $1 target lock
// $2 owner lock
// $3 start slot 
// $4 start amount
// $5 locked until slot
func delegationLock2: and(
	mustSize($0,1),
           // only sizes are enforced, otherwise $3 and $4 are auxiliary, for information
	require(and(equal(len($3),u64/5), equal(len($4),u64/8)), !!!args_$3_and_$4_must_be_5_and_8_bytes_length), 
    require(not(isBranchTransaction), !!!delegation_should_not_be_branch),
	enforceMinimumStorageDeposit,
    concat($0,$1,$2,$3,$4,$5)
)
`
