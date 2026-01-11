package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type DeadlineLock struct {
	Deadline         uint32
	ConstraintMain   Accountable
	ConstraintExpiry Accountable
}

const (
	DeadlineLockName     = "deadlineLock"
	deadlineLockTemplate = DeadlineLockName + "(u32/%d, %s, %s)"
)

func NewDeadlineLock(deadline uint32, main, expiry Accountable) *DeadlineLock {
	return &DeadlineLock{
		Deadline:         deadline,
		ConstraintMain:   main,
		ConstraintExpiry: expiry,
	}
}

func (dl *DeadlineLock) Source() string {
	return fmt.Sprintf(deadlineLockTemplate,
		dl.Deadline,
		dl.ConstraintMain.String(),
		dl.ConstraintExpiry.String(),
	)
}

func (dl *DeadlineLock) Bytes() []byte {
	return mustBinFromSource(dl.Source())
}

func (dl *DeadlineLock) String() string {
	return fmt.Sprintf("%s(%d,%s,%s)", DeadlineLockName, dl.Deadline, dl.ConstraintMain.String(), dl.ConstraintExpiry.String())
}

func (dl *DeadlineLock) Accounts() []Accountable {
	return []Accountable{dl.ConstraintMain, dl.ConstraintExpiry}
}

func (dl *DeadlineLock) Name() string {
	return DeadlineLockName
}

func (dl *DeadlineLock) Master() Accountable {
	return nil
}

func registerDeadlineLockConstraint(lib *Library) {
	lib.mustRegisterConstraint(DeadlineLockName, 3, func(data []byte) (Constraint, error) {
		return DeadlineLockFromBytes(data)
	}, initTestDeadlineLockConstraint)
	lib.mustRegisterLock(DeadlineLockName, func(bytes []byte) (Lock, error) {
		ret, err := DeadlineLockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func initTestDeadlineLockConstraint() {
	addr0 := AddressED25519Random()
	addr1 := AddressED25519Random()

	example := NewDeadlineLock(1337, addr0, addr1)
	lockBack, err := DeadlineLockFromBytes(example.Bytes())
	util.AssertNoError(err)

	util.Assertf(EqualConstraints(lockBack.ConstraintMain, addr0), "inconsistency "+DeadlineLockName)
	util.Assertf(EqualConstraints(lockBack.ConstraintExpiry, addr1), "inconsistency "+DeadlineLockName)

	_, err = L(base.MaxSlot).ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)
}

// DeadlineLockFromBytesAtSlot parses a DeadlineLock using the library for the given slot.
func DeadlineLockFromBytesAtSlot(data []byte, slot uint32) (*DeadlineLock, error) {
	lib := L(slot)
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 3)
	if err != nil {
		return nil, err
	}
	ret := &DeadlineLock{}
	slotBin := easyfl.StripDataPrefix(args[0])
	if sym != DeadlineLockName || len(slotBin) != base.SlotByteLength {
		return nil, fmt.Errorf("can't parse deadline lock")
	}
	parsedSlot, err := base.SlotFromBytes(slotBin)
	if err != nil {
		return nil, err
	}
	ret.Deadline = parsedSlot
	if ret.ConstraintMain, err = AccountableFromBytesAtSlot(args[1], slot); err != nil {
		return nil, err
	}
	if ret.ConstraintExpiry, err = AccountableFromBytesAtSlot(args[2], slot); err != nil {
		return nil, err
	}
	return ret, nil
}

// DeadlineLockFromBytes parses a DeadlineLock using the latest library version.
// Deprecated: Use DeadlineLockFromBytesAtSlot for parsing historical bytecode.
func DeadlineLockFromBytes(data []byte) (*DeadlineLock, error) {
	return DeadlineLockFromBytesAtSlot(data, base.MaxSlot)
}

const deadlineLockSource = `
// $0 - deadline time slot
// $1 - accountable lock before deadline
// $2 - accountable lock at deadline and after
func deadlineLock: if(
	selfIsConsumedOutput,
	conditionalLock(
		lessThan($0, txSlot), $1,
		not(lessThan($0, txSlot)), $2,
		0x, 0x,
		0x, 0x
	),
	mustValidTimeSlot($0)
)
`
