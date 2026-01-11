package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
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
			lessOrEqualThan($0, txSlot)
		) 
	)
)
`

const (
	TimelockName     = "timelock"
	timelockTemplate = TimelockName + "(u32/%d)"
)

type Timelock uint32

var NilTimelock = Timelock(0)

func NewTimelock(timeSlot uint32) Timelock {
	return Timelock(timeSlot)
}

func (t Timelock) Name() string {
	return TimelockName
}

func (t Timelock) Bytes() []byte {
	return mustBinFromSource(t.Source())
}

func (t Timelock) String() string {
	return fmt.Sprintf("%s(%d)", TimelockName, t)
}

func (t Timelock) Source() string {
	return fmt.Sprintf(timelockTemplate, t)
}

// TimelockFromBytesAtSlot parses a Timelock constraint using the library for the given slot.
func TimelockFromBytesAtSlot(data []byte, slot uint32) (Timelock, error) {
	sym, _, args, err := L(slot).ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return NilTimelock, err
	}
	if sym != TimelockName {
		return NilTimelock, fmt.Errorf("not a timelock constraint")
	}
	tlBin := easyfl.StripDataPrefix(args[0])
	ret, err := base.SlotFromBytes(tlBin)
	if err != nil {
		return NilTimelock, err
	}
	return Timelock(ret), nil
}

// TimelockFromBytes parses a Timelock constraint using the latest library version.
// Deprecated: Use TimelockFromBytesAtSlot for parsing historical bytecode.
func TimelockFromBytes(data []byte) (Timelock, error) {
	return TimelockFromBytesAtSlot(data, base.MaxSlot)
}

func registerTimeLockConstraint(lib *Library) {
	lib.mustRegisterConstraint(TimelockName, 1, func(data []byte) (Constraint, error) {
		return TimelockFromBytes(data)
	}, initTestTimelockConstraint)
}

func initTestTimelockConstraint() {
	lib := L(base.MaxSlot)
	example := NewTimelock(1337)
	sym, _, args, err := lib.ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	tlBin := easyfl.StripDataPrefix(args[0])
	e, err := base.SlotFromBytes(tlBin)
	util.AssertNoError(err)

	util.Assertf(sym == TimelockName && e == 1337, "inconsistency in 'timelock'")
}
