package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

type DeadlineLock struct {
	Deadline         Time
	ConstraintMain   Accountable
	ConstraintExpiry Accountable
}

const (
	DeadlineLockName     = "deadlineLock"
	deadlineLockTemplate = DeadlineLockName + "(u32/%d, %d, x/%s, x/%s)"
)

func NewDeadlineLock(deadline Time, main, expiry Accountable) *DeadlineLock {
	return &DeadlineLock{
		Deadline:         deadline,
		ConstraintMain:   main,
		ConstraintExpiry: expiry,
	}
}

func (dl *DeadlineLock) source() string {
	return fmt.Sprintf(deadlineLockTemplate,
		dl.Deadline.Slot(),
		dl.Deadline.Tick(),
		hex.EncodeToString(dl.ConstraintMain.AccountID()),
		hex.EncodeToString(dl.ConstraintExpiry.AccountID()),
	)
}

func (dl *DeadlineLock) Bytes() []byte {
	return mustBinFromSource(dl.source())
}

func (dl *DeadlineLock) String() string {
	return fmt.Sprintf("%s(%d,%d,%s,%s)", DeadlineLockName, dl.Deadline.Slot(), dl.Deadline.Tick(), dl.ConstraintMain, dl.ConstraintExpiry)
}

func (dl *DeadlineLock) Accounts() []Accountable {
	return []Accountable{dl.ConstraintMain, dl.ConstraintExpiry}
}

func (dl *DeadlineLock) UnlockableWith(acc AccountID, ts ...Time) bool {
	if len(ts) == 0 {
		return bytes.Equal(dl.ConstraintMain.AccountID(), acc) || bytes.Equal(dl.ConstraintExpiry.AccountID(), acc)
	}
	if ts[0].Before(dl.Deadline) {
		return bytes.Equal(dl.ConstraintMain.AccountID(), acc)
	}
	return bytes.Equal(dl.ConstraintExpiry.AccountID(), acc)
}

func (dl *DeadlineLock) Name() string {
	return DeadlineLockName
}

func addDeadlineLockConstraint(lib *Library) {
	lib.extendWithConstraint(DeadlineLockName, deadlineLockSource, 4, func(data []byte) (Constraint, error) {
		return DeadlineLockFromBytes(data)
	})
}

func initTestDeadlineLockConstraint() {
	ts := MustNewLedgerTime(1337, 5)
	example := NewDeadlineLock(ts, AddressED25519Null(), AddressED25519Null())
	lockBack, err := DeadlineLockFromBytes(example.Bytes())
	util.AssertNoError(err)

	util.Assertf(EqualConstraints(lockBack.ConstraintMain, AddressED25519Null()), "inconsistency "+DeadlineLockName)
	util.Assertf(EqualConstraints(lockBack.ConstraintExpiry, AddressED25519Null()), "inconsistency "+DeadlineLockName)

	_, err = L().ParseBytecodePrefix(example.Bytes())
	util.AssertNoError(err)
}

func DeadlineLockFromBytes(data []byte) (*DeadlineLock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 4)
	if err != nil {
		return nil, err
	}
	ret := &DeadlineLock{}
	slotBin := easyfl.StripDataPrefix(args[0])
	tickBin := easyfl.StripDataPrefix(args[1])
	if sym != DeadlineLockName || len(slotBin) != SlotByteLength || len(tickBin) != 1 {
		return nil, fmt.Errorf("can't parse deadline lock")
	}
	slot, err := SlotFromBytes(slotBin)
	if err != nil {
		return nil, err
	}
	tick, err := TickFromByte(tickBin[0])
	if err != nil {
		return nil, err
	}
	ret.Deadline = MustNewLedgerTime(slot, tick)
	if ret.ConstraintMain, err = AccountableFromBytes(args[2]); err != nil {
		return nil, err
	}
	if ret.ConstraintExpiry, err = AccountableFromBytes(args[3]); err != nil {
		return nil, err
	}
	return ret, nil
}

const deadlineLockSource = `

func deadlineLock: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	selfMustStandardAmount,
    mustValidTimeSlot($0),
    mustValidTimeTick($1),
	if(
		ticksBefore(txTimestampBytes, timestamp($0,$1)),
		$2, 
		$3
	)
)
`
