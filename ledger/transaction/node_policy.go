package transaction

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
