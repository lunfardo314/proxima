package utxodb

import (
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

	commands := make([]multistate.UpdateCmd, 0)
	tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		commands = append(commands, multistate.UpdateCmd{
			ID: oid,
		})
		return true
	})
	tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
		commands = append(commands, multistate.UpdateCmd{
			ID:     oid,
			Output: o,
		})
		return true
	})
	//fmt.Printf("Commands:\n%s\n", state.UpdateCommandsToString(commands))
	if err = u.UpdateWithCommands(commands, nil, nil); err != nil {
		return nil, err
	}
	return tx, nil
}
