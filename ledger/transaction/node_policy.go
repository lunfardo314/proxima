package transaction

import (
	"bytes"
	"slices"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// CheckNodePolicy runs the node's Go policy checks on a transaction with the full context set,
// in the order the node applies them after ValidateFullContext. The vertex and the API's
// validate_only path both call it, so a dry run refuses what attachment refuses.
func (tx *Transaction) CheckNodePolicy() error {
	for _, check := range []func() error{
		tx.CheckDelegationTransitions,
		tx.CheckStemAndTargetUnlocks,
		tx.CheckDelegationStates,
		tx.CheckFrozenCoverageBound,
		tx.CheckIssuanceBound,
	} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// DepositExemptOutputsArePlain reports whether every produced output whose lock is exempt from the
// storage deposit (tag-along, sendWithDeadline) carries only what its lock reads: exactly the lock's
// index values and no chain constraint. Such an output pays no deposit, so anything more on it would
// be index entries or a chain in the state at no cost. Applied at ingress to unsolicited transactions
// only, never in CheckNodePolicy: a transaction a past cone needs is attached whatever it carries.
func (tx *Transaction) DepositExemptOutputsArePlain() bool {
	ret := true
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, _ base.OutputID) bool {
		switch lock := o.Lock().(type) {
		case *ledger.TagAlongLock, *ledger.SendWithDeadlineLock:
			ret = o.ChainConstraint() == nil && slices.EqualFunc(o.IndexValues(), lock.IndexValues(), bytes.Equal)
		}
		return ret
	})
	return ret
}
