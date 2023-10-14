package utxodb

import (
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
	if err = u.Update(muts, nil, nil, 0); err != nil {
		return nil, err
	}
	return tx, nil
}
