package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

type DeadlineLock struct {
	Deadline         Slot
	ConstraintMain   Accountable
	ConstraintExpiry Accountable
}

const (
	DeadlineLockName     = "deadlineLock"
	deadlineLockTemplate = DeadlineLockName + "(u32/%d, %s, %s)"
)

func NewDeadlineLock(deadline Slot, main, expiry Accountable) *DeadlineLock {
	return &DeadlineLock{
		Deadline:         deadline,
		ConstraintMain:   main,
		ConstraintExpiry: expiry,
	}
}

func (dl *DeadlineLock) source() string {
	return fmt.Sprintf(deadlineLockTemplate,
		dl.Deadline,
		dl.ConstraintMain.String(),
		dl.ConstraintExpiry.String(),
	)
}

func (dl *DeadlineLock) Bytes() []byte {
	return mustBinFromSource(dl.source())
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

func addDeadlineLockConstraint(lib *Library) {
	lib.extendWithConstraint(DeadlineLockName, deadlineLockSource, 3, func(data []byte) (Constraint, error) {
		return DeadlineLockFromBytes(data)
	}, initTestDeadlineLockConstraint)
}

func initTestDeadlineLockConstraint() {
	addr0 := AddressED25519Random()
	addr1 := AddressED25519Random()

	example := NewDeadlineLock(1337, addr0, addr1)
	lockBack, err := DeadlineLockFromBytes(example.Bytes())
	util.AssertNoError(err)

	util.Assertf(EqualConstraints(lockBack.ConstraintMain, addr0), "inconsistency "+DeadlineLockName)
	util.Assertf(EqualConstraints(lockBack.ConstraintExpiry, addr1), "inconsistency "+DeadlineLockName)

	_, err = L().ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)
}

func DeadlineLockFromBytes(data []byte) (*DeadlineLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 3)
	if err != nil {
		return nil, err
	}
	ret := &DeadlineLock{}
	slotBin := easyfl.StripDataPrefix(args[0])
	if sym != DeadlineLockName || len(slotBin) != SlotByteLength {
		return nil, fmt.Errorf("can't parse deadline lock")
	}
	slot, err := SlotFromBytes(slotBin)
	if err != nil {
		return nil, err
	}
	ret.Deadline = slot
	if ret.ConstraintMain, err = AccountableFromBytes(args[1]); err != nil {
		return nil, err
	}
	if ret.ConstraintExpiry, err = AccountableFromBytes(args[2]); err != nil {
		return nil, err
	}
	return ret, nil
}

const deadlineLockSource = `
// $0 - deadline time slot
// $1 - accountable lock before deadline
// $2 - accountable lock at deadline and after
func deadlineLock: if(
	selfIsConsumedOutput,
	conditionalLock(
		lessThan($0, txTimeSlot), $1,
		not(lessThan($0, txTimeSlot)), $2,
		0x, 0x,
		0x, 0x
	),
	mustValidTimeSlot($0)
)
`
