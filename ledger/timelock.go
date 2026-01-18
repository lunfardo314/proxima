package ledger

import (
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

//go:embed def/timelock.efl
var timelockSource string

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

// TimelockFromBytesWithLib parses a Timelock constraint using the library for the given slot.
func TimelockFromBytesWithLib(data []byte, lib *Library) (Timelock, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 1)
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

func registerTimeLockConstraint(lib *Library) {
	lib.mustRegisterConstraint(TimelockName, 1, func(data []byte) (Constraint, error) {
		return TimelockFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		example := NewTimelock(1337)
		sym, _, args, err := lib.ParseBytecodeOneLevel(example.Bytes(), 1)
		util.AssertNoError(err)
		tlBin := easyfl.StripDataPrefix(args[0])
		e, err := base.SlotFromBytes(tlBin)
		util.AssertNoError(err)

		util.Assertf(sym == TimelockName && e == 1337, "inconsistency in 'timelock'")
	})
}
