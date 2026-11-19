package transaction

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// DelegationStateCheckFromSlot is the first slot whose transactions a node checks with
// CheckDelegationStates (12:00:10 UTC 2026-11-19). Earlier history is not re-checked.
var DelegationStateCheckFromSlot uint32 = 271921

// CheckDelegationStates refuses a transaction producing a delegation that is not of the fixed
// layout: exactly amounts, index values, lock, chain constraint and a delegateLockState the node
// can parse.
//
// The state: the EasyFL rule bounds the last frozen epoch and the advance share by value only,
// while the frozen-coverage builtin reads them through the Go parser, which bounds them by length
// and reads a state it cannot parse as undef. A hold encoding a zero epoch in more than 4 bytes
// passes the rule as a hold and the builtin as no hold, so the target is never debited.
//
// The layout: anyone may create a delegation naming any master and target, and the target must
// keep and evaluate on every transition whatever constraint sits between the chain constraint and
// the state, so such a constraint can stall the target or lock the master's funds.
//
// Every honest builder produces the fixed layout with a canonical state (z32, 1 byte, z16). Node
// policy for transactions from DelegationStateCheckFromSlot, until the ledger enforces both.
func (tx *Transaction) CheckDelegationStates() error {
	slot := tx.Slot()
	if slot < DelegationStateCheckFromSlot {
		return nil
	}
	var err error
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, _ base.OutputID) bool {
		if o.Lock().Name() != ledger.DelegateLockName {
			return true
		}
		if n := o.NumElements(); n != ledger.DelegationNumElements {
			err = fmt.Errorf("CheckDelegationStates: delegation output %d has %d elements, expected %d", idx, n, ledger.DelegationNumElements)
			return false
		}
		last, err1 := o.ConstraintAt(ledger.DelegationNumElements - 1)
		if err1 == nil {
			_, err1 = ledger.DelegateLockStateFromBytesWithLib(last, tx.Library)
		}
		if err1 != nil {
			err = fmt.Errorf("CheckDelegationStates: delegation output %d carries a non-canonical state: %w", idx, err1)
			return false
		}
		return true
	})
	return err
}
