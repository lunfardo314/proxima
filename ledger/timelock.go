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
	timelockName     = "timelock"
	timelockTemplate = timelockName + "(u32/%d)"
)

type Timelock Slot

var NilTimelock = Timelock(0)

func NewTimelock(timeSlot Slot) Timelock {
	return Timelock(timeSlot)
}

func (t Timelock) Name() string {
	return timelockName
}

func (t Timelock) Bytes() []byte {
	return mustBinFromSource(t.source())
}

func (t Timelock) String() string {
	return fmt.Sprintf("%s(%d)", timelockName, t)
}

func (t Timelock) source() string {
	return fmt.Sprintf(timelockTemplate, t)
}

func TimelockFromBytes(data []byte) (Timelock, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return NilTimelock, err
	}
	if sym != timelockName {
		return NilTimelock, fmt.Errorf("not a timelock constraint")
	}
	tlBin := easyfl.StripDataPrefix(args[0])
	ret, err := TimeSlotFromBytes(tlBin)
	if err != nil {
		return NilTimelock, err
	}
	return Timelock(ret), nil
}

func initTimelockConstraint() {
	easyfl.MustExtendMany(timelockSource)

	example := NewTimelock(1337)
	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	tlBin := easyfl.StripDataPrefix(args[0])
	e, err := TimeSlotFromBytes(tlBin)
	util.AssertNoError(err)

	util.Assertf(sym == timelockName && e == 1337, "inconsistency in 'timelock'")

	registerConstraint(timelockName, prefix, func(data []byte) (Constraint, error) {
		return TimelockFromBytes(data)
	})
}
