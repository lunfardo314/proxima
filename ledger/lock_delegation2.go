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
	MasterLock Accountable
	// must point to the sibling chain constraint
	ChainConstraintIndex    byte
	MaxLockCoverageSlots    byte
	LockedCoverageUntilSlot base.Slot
	StartSlot               base.Slot
	StartAmount             uint64
}

const (
	DelegationLock2Name       = "delegationLock2"
	delegationLock2TemplateHR = DelegationLock2Name + "(chainIdx=%d, target=%s, master=%s, maxCoverageLockSlots=%d, startSlot=%d, startAmount=%s, lockedCoverageUntil=%d)"
	delegationLock2Template   = DelegationLock2Name + "(%d, %s, %s, %d, z64/%d, z32/%d, z32/%d)"
)

func NewDelegationLock2(chainConstraintIndex byte, owner, target Accountable, maxCoverageLockSlots byte, startSlot base.Slot, startAmount uint64) *DelegationLock2 {
	return &DelegationLock2{
		TargetLock:              target,
		MasterLock:              owner,
		ChainConstraintIndex:    chainConstraintIndex,
		StartSlot:               startSlot,
		StartAmount:             startAmount,
		LockedCoverageUntilSlot: 0,
		MaxLockCoverageSlots:    maxCoverageLockSlots,
	}
}

func DelegationLock2FromBytes(data []byte) (*DelegationLock2, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 7)
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
	// coverage locked until slot
	locCov, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[6]))
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
		d.ChainConstraintIndex, d.TargetLock.Source(), d.MasterLock.Source(), d.MaxLockCoverageSlots, d.StartSlot, d.StartAmount, d.LockedCoverageUntilSlot)
}

func (d *DelegationLock2) String() string {
	return fmt.Sprintf(delegationLock2TemplateHR,
		d.ChainConstraintIndex, d.TargetLock.Source(), d.MasterLock.Source(), d.MaxLockCoverageSlots, d.StartSlot, util.Th(d.StartAmount), d.LockedCoverageUntilSlot)
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
	lib.mustRegisterConstraint(DelegationLock2Name, 7, func(data []byte) (Constraint, error) {
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
	example.LockedCoverageUntilSlot = base.Slot(10000)

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
	util.Assertf(example.LockedCoverageUntilSlot == 10000, "DelegationLock2FromBytes: wrong back")
	util.Assertf(example.LockedCoverageUntilSlot == exampleBack.LockedCoverageUntilSlot, "DelegationLock2FromBytes: wrong back")

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
// $2 master lock
// $3 max locked coverage slots
// $4 start slot 
// $5 start amount
// $6 locked until slot
func delegationLock2: and(
	require( and( equalUint(len($0),1), equalUint(len($3),1)), !!!wrong_arg_sizes ), 
    require(not(isBranchTransaction), !!!delegation_should_not_be_branch),
	enforceMinimumStorageDeposit,
    concat($0,$1,$2,$3,$4,$5,$6)
)
`
