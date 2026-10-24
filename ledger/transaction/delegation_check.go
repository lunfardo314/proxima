package transaction

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// DelegationCheckFromSlot is the first slot whose transactions a node checks with
// CheckDelegationTransitions. Earlier history is not re-checked.
const DelegationCheckFromSlot = 52000

// CheckDelegationTransitions applies, ahead of library upgrade 3, the delegation
// rules that upgrade 3 puts into the ledger: a delegation is produced frozen only by
// its target, from a delegation, and the target puts on hold or tops up a frozen
// delegation only when it carries frozen coverage. It is node policy for
// transactions from DelegationCheckFromSlot up to the upgrade slot; from the slot
// on, the ledger enforces the same. Requires the full context.
func (tx *Transaction) CheckDelegationTransitions() error {
	slot := tx.Slot()
	if slot < DelegationCheckFromSlot || slot >= ledger.Upgrade3Slot {
		return nil
	}
	var err error
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid base.OutputID) bool {
		d, ok := ledger.AsDelegationOutput(o, oid)
		if !ok || !d.IsMarkedFrozen() || d.IsOrigin() {
			return true
		}
		if !tx.isUnlockedByTarget(d.PredecessorInputIndex) {
			err = fmt.Errorf("CheckDelegationTransitions: delegation %s is frozen not by its target", d.ChainID.StringShort())
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	for i := 0; i < tx.NumInputs(); i++ {
		in, err1 := tx.ConsumedOutputAt(byte(i))
		if err1 != nil {
			return err1
		}
		d, ok := ledger.AsDelegationOutput(in.Output, in.ID)
		if !ok || !d.IsMarkedFrozen() || !d.IsInFrozenSlot(slot) || !tx.isUnlockedByTarget(byte(i)) {
			continue
		}
		if in.Output.Amounts().IsFrozenCoverageZero() {
			return fmt.Errorf("CheckDelegationTransitions: inconsistent frozen delegation %s", d.ChainID.StringShort())
		}
	}
	return nil
}

// isUnlockedByTarget is true when the consumed output at idx is a delegation
// unlocked by its target, not by its master.
func (tx *Transaction) isUnlockedByTarget(idx byte) bool {
	in, err := tx.ConsumedOutputAt(idx)
	if err != nil || in.Output.Lock().Name() != ledger.DelegateLockName {
		return false
	}
	unlock, err := tx.UnlockParameters(idx, ledger.ConstraintIndexLock)
	return err == nil && len(unlock) >= 2 && unlock[1] != ledger.DelegationUnlockedByMaster
}
