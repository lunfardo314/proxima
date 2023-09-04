package utxodb

import (
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/state"
)

func updateValidateNoDebug(u *state.Updatable, txBytes []byte) (*state.Transaction, error) {
	return updateValidateOptions(u, txBytes, state.TraceOptionNone, nil)
}

func updateValidateDebug(u *state.Updatable, txBytes []byte, onValidation ...func(ctx *state.TransactionContext, err error) error) (*state.Transaction, error) {
	var fun func(ctx *state.TransactionContext, err error) error
	if len(onValidation) > 0 {
		fun = onValidation[0]
	}
	return updateValidateOptions(u, txBytes, state.TraceOptionFailedConstraints, fun)
}

// updateValidateNoDebug updates/mutates the ledger state by transaction. For testing mostly
func updateValidateOptions(u *state.Updatable, txBytes []byte, traceOption int, onValidation func(ctx *state.TransactionContext, err error) error) (*state.Transaction, error) {
	tx, err := state.TransactionFromBytesAllChecks(txBytes)
	if err != nil {
		return nil, err
	}
	ctx, err := state.TransactionContextFromTransaction(tx, tx.InputLoaderByIndex(u.Readable().GetUTXO), traceOption)
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

	commands := make([]state.UpdateCmd, 0)
	tx.ForEachInput(func(i byte, oid *core.OutputID) bool {
		commands = append(commands, state.UpdateCmd{
			ID: oid,
		})
		return true
	})
	tx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
		commands = append(commands, state.UpdateCmd{
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
