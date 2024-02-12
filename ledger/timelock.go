package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

const timelockSource = `
// enforces output can be unlocked only after specified time slot is reached
// $0 is time slot
func timelock: and(
    mustValidTimeSlot($0),
	or(
		selfIsProducedOutput, 
		and( 
			selfIsConsumedOutput,
			lessOrEqualThan($0, timeSlotPrefix(txTimestampBytes))
		) 
	)
)
`

const (
	TimelockName     = "timelock"
	timelockTemplate = TimelockName + "(u32/%d)"
)

type Timelock Slot

var NilTimelock = Timelock(0)

func NewTimelock(timeSlot Slot) Timelock {
	return Timelock(timeSlot)
}

func (t Timelock) Name() string {
	return TimelockName
}

func (t Timelock) Bytes() []byte {
	return mustBinFromSource(t.source())
}

func (t Timelock) String() string {
	return fmt.Sprintf("%s(%d)", TimelockName, t)
}

func (t Timelock) source() string {
	return fmt.Sprintf(timelockTemplate, t)
}

func TimelockFromBytes(data []byte) (Timelock, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return NilTimelock, err
	}
	if sym != TimelockName {
		return NilTimelock, fmt.Errorf("not a timelock constraint")
	}
	tlBin := easyfl.StripDataPrefix(args[0])
	ret, err := SlotFromBytes(tlBin)
	if err != nil {
		return NilTimelock, err
	}
	return Timelock(ret), nil
}

func addTimeLockConstraint(lib *Library) {
	lib.extendWithConstraint(TimelockName, timelockSource, 1, func(data []byte) (Constraint, error) {
		return TimelockFromBytes(data)
	})
}

func initTestTimelockConstraint() {
	example := NewTimelock(1337)
	sym, _, args, err := L().ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	tlBin := easyfl.StripDataPrefix(args[0])
	e, err := SlotFromBytes(tlBin)
	util.AssertNoError(err)

	util.Assertf(sym == TimelockName && e == 1337, "inconsistency in 'timelock'")
}
