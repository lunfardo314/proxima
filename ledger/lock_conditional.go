package ledger

import (
	"fmt"
	"strings"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// TODO experimental not tested !!!!!

// ConditionalLock enforces condition by selecting first of up to 4 satisfied conditions and evaluating
// corresponding accountable lock
type ConditionalLock struct {
	Conditions [4]Constraint
	Locks      [4]Accountable
}

const ConditionalLockName = "conditionalLock"

const conditionalLockSource = `
// $0, $2, $4, $6 - four conditions
// $1, $3, $5, $7 - four locks
// 
func conditionalLock: selectCaseByIndex(
	firstCaseIndex($0, $2, $4, $6), 
	$1, $3, $5, $7
) 
`

func NewConditionalLock(conds []Constraint, locks []Accountable) (*ConditionalLock, error) {
	if len(conds) == 0 || len(conds) > 4 || len(conds) != len(locks) {
		return nil, fmt.Errorf("must be 1 to 4 condition/lock pairs")
	}
	ret := &ConditionalLock{}
	for i, c := range conds {
		ret.Conditions[i] = c
		if locks[i].Name() == StemLockName {
			return nil, fmt.Errorf("stemLock is not allowed in the conditional lock")
		}
		ret.Locks[i] = locks[i]
	}
	return ret, nil
}

// ConditionalLockFromBytesAtSlot parses a ConditionalLock using the library for the given slot.
func ConditionalLockFromBytesAtSlot(data []byte, slot uint32) (*ConditionalLock, error) {
	sym, _, args, err := L(slot).ParseBytecodeOneLevel(data, 8)
	if err != nil {
		return nil, err
	}
	if sym != ConditionalLockName {
		return nil, fmt.Errorf("not a ConditionalLock")
	}
	ret := &ConditionalLock{}
	for i := range [4]byte{} {
		if len(args[i]) > 0 {
			if ret.Conditions[i], err = ConstraintFromBytesAtSlot(args[i], slot); err != nil {
				return nil, err
			}
		}
		if len(args[i+1]) > 0 {
			if ret.Locks[i], err = AccountableFromBytesAtSlot(args[i+1], slot); err != nil {
				return nil, err
			}
			if ret.Locks[i].Name() == StemLockName {
				return nil, fmt.Errorf("stemLock is not allowed in the conditional lock")
			}
		}
	}
	return ret, nil
}

func (c *ConditionalLock) Source() string {
	args := make([]string, 8)
	for i := range c.Conditions {
		if util.IsNil(c.Conditions[i]) {
			args[i] = "0x"
			args[i+1] = "0x"
			continue
		}
		args[i] = c.Conditions[i].String()
		if util.IsNil(c.Locks[i]) {
			args[i+1] = "0x"
		} else {
			args[i+1] = c.Locks[i].String()
		}
	}
	return fmt.Sprintf("conditionalLock(%s)", strings.Join(args, ","))
}

func (c *ConditionalLock) Bytes() []byte {
	return mustBinFromSource(c.Source())
}

func (c *ConditionalLock) Accounts() []Accountable {
	return NoDuplicatesAccountables(c.Locks[:])
}

func (c *ConditionalLock) Name() string {
	return ConditionalLockName
}

func (c *ConditionalLock) String() string {
	return c.Source()
}

func (c *ConditionalLock) Master() Accountable {
	return nil
}

func registerConditionalLock(lib *Library) {
	lib.mustRegisterConstraint(ConditionalLockName, 8, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return ConditionalLockFromBytesAtSlot(data, base.MaxSlot)
	})
	lib.mustRegisterLock(ConditionalLockName, func(bytes []byte) (Lock, error) {
		// Use latest library version for library registration parsing
		ret, err := ConditionalLockFromBytesAtSlot(bytes, base.MaxSlot)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}
