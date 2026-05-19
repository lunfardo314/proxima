package utxodb

import (
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

func updateValidateNoDebug(u *multistate.Updatable, txBytes []byte) (*transaction.Transaction, error) {
	return updateValidate(u, txBytes, nil)
}

func updateValidateDebug(u *multistate.Updatable, txBytes []byte, onValidation ...func(ctx *transaction.Transaction, err error) error) (*transaction.Transaction, error) {
	var fun func(ctx *transaction.Transaction, err error) error
	if len(onValidation) > 0 {
		fun = onValidation[0]
	}
	return updateValidate(u, txBytes, fun)
}

// updateValidate updates/mutates the ledger state by transaction. For testing mostly
func updateValidate(u *multistate.Updatable, txBytes []byte, onValidation func(tx *transaction.Transaction, err error) error) (*transaction.Transaction, error) {
	tx, err := transaction.Parse(txBytes)
	if err != nil {
		return nil, err
	}

	if err = tx.SetFullContext(tx.InputLoaderByIndex(u.Readable().GetUTXO)); err != nil {
		return nil, err
	}
	err = tx.ValidateFullContext()
	if onValidation != nil {
		err = onValidation(tx, err)
	}
	if err != nil {
		return nil, err
	}

	muts := tx.StateMutations()
	if err = ConsistencyCheckBeforeAddTransaction(tx, u.Readable()); err != nil {
		return nil, err
	}

	err = u.Update(muts, nil)
	if err != nil {
		return nil, err
	}

	if err = ConsistencyCheckAfterAddTransaction(tx, u.Readable()); err != nil {
		return nil, err
	}
	return tx, nil
}

// ConsistencyCheckBeforeAddTransaction redundant?
// TODO check account consistency
func ConsistencyCheckBeforeAddTransaction(tx *transaction.Transaction, r *multistate.Readable) (err error) {
	if r.KnowsCommittedTransaction(tx.ID()) {
		return fmt.Errorf("BeforeAddTransaction: transaction %s already in the state: cannot be added", tx.IDShortString())
	}
	tx.ForEachInputID(func(i byte, oid base.OutputID) bool {
		if !r.HasUTXO(oid) {
			err = fmt.Errorf("BeforeAddTransaction: output %s does not exist: cannot be consumed", oid.StringShort())
			return false
		}
		return true
	})

	var chainInput base.OutputID
	var oData *ledger.OutputDataWithID

	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
		if r.HasUTXO(oid) {
			err = fmt.Errorf("BeforeAddTransaction: output %s already exist: cannot be produced", oid.StringShort())
			return false
		}
		chainConstraint := o.ChainConstraint()
		if chainConstraint == nil {
			return true
		}
		if chainConstraint.IsOrigin() {
			// chain records should not exist
			chainID := base.MakeOriginChainID(oid)
			_, err = r.GetUTXOForChainID(chainID)
			if errors.Is(err, multistate.ErrNotFound) {
				return true
			}
			err = fmt.Errorf("BeforeAddTransaction: chainID %s should not be present in the state", chainID.StringShort())
			return false
		}

		// chain record must exist and must be consistent with chain input
		oData, err = r.GetUTXOForChainID(chainConstraint.ChainID)
		if err != nil {
			err = fmt.Errorf("BeforeAddTransaction: chainID %s should be present in the state", chainConstraint.ChainID.StringShort())
			return false
		}
		chainInput = tx.MustInputAt(chainConstraint.PredecessorInputIndex)
		if chainInput != oData.ID {
			err = fmt.Errorf("BeforeAddTransaction: inconsistent chain input with chain record for chain %s", chainConstraint.ChainID.StringShort())
			return false
		}
		return true
	})
	return nil
}

func ConsistencyCheckAfterAddTransaction(tx *transaction.Transaction, r *multistate.Readable) (err error) {
	if !r.KnowsCommittedTransaction(tx.ID()) {
		return fmt.Errorf("AfterAddTransaction: transaction %s is expected to be in the state", tx.IDShortString())
	}
	tx.ForEachInputID(func(i byte, oid base.OutputID) bool {
		if r.HasUTXO(oid) {
			err = fmt.Errorf("input %s must not exist", oid.StringShort())
			return false
		}
		return true
	})

	var oData *ledger.OutputDataWithID
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
		if !r.HasUTXO(oid) {
			err = fmt.Errorf("AfterAddTransaction: output %s must exist", oid.StringShort())
			return false
		}
		chainConstraint := o.ChainConstraint()
		if chainConstraint == nil {
			return true
		}
		var chainID base.ChainID
		if chainConstraint.IsOrigin() {
			chainID = base.MakeOriginChainID(oid)
		} else {
			chainID = chainConstraint.ChainID
		}
		oData, err = r.GetUTXOForChainID(chainID)
		if err != nil {
			err = fmt.Errorf("AfterAddTransaction: chainID %s should be present in the state", chainID.StringShort())
			return false
		}
		if oid != oData.ID {
			err = fmt.Errorf("AfterAddTransaction: inconsistent chain output with chain record for chain %s", chainID.StringShort())
			return false
		}
		return true
	})
	return nil
}
