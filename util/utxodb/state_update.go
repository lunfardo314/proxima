package utxodb

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
)

func updateValidateNoDebug(u *multistate.Updatable, txBytes []byte) (*transaction.Transaction, error) {
	return updateValidateOptions(u, txBytes, transaction.TraceOptionNone, nil)
}

func updateValidateDebug(u *multistate.Updatable, txBytes []byte, onValidation ...func(ctx *transaction.TransactionContext, err error) error) (*transaction.Transaction, error) {
	var fun func(ctx *transaction.TransactionContext, err error) error
	if len(onValidation) > 0 {
		fun = onValidation[0]
	}
	return updateValidateOptions(u, txBytes, transaction.TraceOptionFailedConstraints, fun)
}

// updateValidateNoDebug updates/mutates the ledger state by transaction. For testing mostly
func updateValidateOptions(u *multistate.Updatable, txBytes []byte, traceOption int, onValidation func(ctx *transaction.TransactionContext, err error) error) (*transaction.Transaction, error) {
	tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
	if err != nil {
		return nil, err
	}
	ctx, err := transaction.ContextFromTransaction(tx, tx.InputLoaderByIndex(u.Readable().GetUTXO), traceOption)
	if err != nil {
		return nil, err
	}
	err = ctx.Validate()
	if onValidation != nil {
		err = onValidation(ctx, err)
	}
	if err != nil {
		return nil, err
	}

	muts := tx.StateMutations()
	if err := ConsistencyCheckBeforeAddTransaction(tx, u.Readable()); err != nil {
		return nil, err
	}

	if err = u.Update(muts, nil, nil, multistate.LedgerCoverage{}); err != nil {
		return nil, err
	}

	if err := ConsistencyCheckAfterAddTransaction(tx, u.Readable()); err != nil {
		return nil, err
	}
	return tx, nil
}

// TODO check account consistency

func ConsistencyCheckBeforeAddTransaction(tx *transaction.Transaction, r *multistate.Readable) (err error) {
	if r.KnowsCommittedTransaction(tx.ID()) {
		return fmt.Errorf("BeforeAddTransaction: transaction %s already in the state: cannot be added", tx.IDShort())
	}
	tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		if !r.HasUTXO(oid) {
			err = fmt.Errorf("BeforeAddTransaction: output %s does not exist: cannot be consumed", oid.Short())
			return false
		}
		return true
	})

	var chainInput core.OutputID
	var oData *core.OutputDataWithID

	tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
		if r.HasUTXO(oid) {
			err = fmt.Errorf("BeforeAddTransaction: output %s already exist: cannot be produced", oid.Short())
			return false
		}
		chainConstraint, i := o.ChainConstraint()
		if i == 0xff {
			return true
		}
		if chainConstraint.IsOrigin() {
			// chain records should not exist
			chainID := core.OriginChainID(oid)
			_, err = r.GetUTXOForChainID(&chainID)
			if errors.Is(err, multistate.ErrNotFound) {
				return true
			}
			err = fmt.Errorf("BeforeAddTransaction: chainID %s should not be present in the state", chainID.Short())
			return false
		}

		// chain record must exist and must be consistent with chain input
		oData, err = r.GetUTXOForChainID(&chainConstraint.ID)
		if err != nil {
			err = fmt.Errorf("BeforeAddTransaction: chainID %s should be present in the state", chainConstraint.ID.Short())
			return false
		}
		chainInput = tx.MustInputAt(chainConstraint.PredecessorInputIndex)
		if chainInput != oData.ID {
			err = fmt.Errorf("BeforeAddTransaction: inconsistent chain input with chain record for chain %s", chainConstraint.ID.Short())
			return false
		}
		return true
	})
	return nil
}

func ConsistencyCheckAfterAddTransaction(tx *transaction.Transaction, r *multistate.Readable) (err error) {
	if !r.KnowsCommittedTransaction(tx.ID()) {
		return fmt.Errorf("AfterAddTransaction: transaction %s is expected to be in the state", tx.IDShort())
	}
	tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		if r.HasUTXO(oid) {
			err = fmt.Errorf("input %s must not exist", oid.Short())
			return false
		}
		return true
	})

	var oData *core.OutputDataWithID
	tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
		if !r.HasUTXO(oid) {
			err = fmt.Errorf("AfterAddTransaction: output %s must exist", oid.Short())
			return false
		}
		chainConstraint, i := o.ChainConstraint()
		if i == 0xff {
			return true
		}
		var chainID core.ChainID
		if chainConstraint.IsOrigin() {
			chainID = core.OriginChainID(oid)
		} else {
			chainID = chainConstraint.ID
		}
		oData, err = r.GetUTXOForChainID(&chainID)
		if err != nil {
			err = fmt.Errorf("AfterAddTransaction: chainID %s should be present in the state", chainID.Short())
			return false
		}
		if *oid != oData.ID {
			err = fmt.Errorf("AfterAddTransaction: inconsistent chain output with chain record for chain %s", chainID.Short())
			return false
		}
		return true
	})
	return nil
}
