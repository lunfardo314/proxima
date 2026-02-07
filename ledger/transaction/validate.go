package transaction

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/slicepool"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

const (
	TraceOptionNone = iota
	TraceOptionAll
	TraceOptionFailedConstraints
)

func (tx *Transaction) makeEvalContext(path []byte) easyfl.GlobalData[*ledger.EvalContext] {
	// Use the transaction's cached library for validation
	dataCtx := ledger.NewEvalContext(tx)
	dataCtx.SetEvalPath(path)
	switch tx.traceOption {
	case TraceOptionNone:
		return tx.NewGlobalDataNoTrace(dataCtx)
	case TraceOptionAll:
		return tx.NewGlobalDataTracePrint(dataCtx)
	case TraceOptionFailedConstraints:
		return tx.Library.NewGlobalDataLog(dataCtx)
	default:
		panic("wrong trace option")
	}
}

// ValidatePartialContext runs all validation scripts (constraints) that only needs partial context,
// i.e. no need for the past cone.
// This is STAGE 2 of the transaction validation
func (tx *Transaction) ValidatePartialContext() error {
	util.Assertf(!tx.partialContextValidated, "repeating run on partial context")

	spool := slicepool.New()
	defer spool.Dispose()

	tx.partialContextValidated = true
	return util.CatchPanicOrError(func() error {
		if err := tx.scanPartialContext(); err != nil {
			return err
		}
		return tx.TxIntegrityValidatorSkeletonContext(tx.makeEvalContext(nil), spool)
	})
}

// ValidateFullContext runs all validation scripts (constraints) that require full context,
// i.e. all consumed UTXOs must be available
// This is STAGE 3 of the transaction validation. It requires STAGE 1 and STAGE 2 successfully passed
func (tx *Transaction) ValidateFullContext() error {
	util.Assertf(!tx.fullContextValidated, "repeating run on full context")

	var err error
	if !tx.partialContextValidated {
		if err = tx.ValidatePartialContext(); err != nil {
			return err
		}
	}
	util.Assertf(tx.partialContextValidated, "repeating run on partial context")

	spool := slicepool.New()
	defer spool.Dispose()

	err = util.CatchPanicOrError(func() error {
		var err1 error
		// run tx integrity validation script that requires full context
		if err1 = tx.TxIntegrityValidatorFullContext(tx.makeEvalContext(nil), spool); err1 != nil {
			return err1
		}
		// run tx level constrains, if any
		if err1 = tx.validateTxLevelConstraints(spool); err1 != nil {
			return err1
		}
		// run all scripts on consumed and produced UTXOs
		if err1 = tx.validateOutputs(spool); err1 != nil {
			return err1
		}
		return nil
	})
	if err != nil {
		return err
	}
	// ledger invariant (consumed + inflation = produced) is enforced in validateOutputs()
	return nil
}

// evalBytecode safely runs the bytecode in the context of the path
func (tx *Transaction) evalBytecode(bytecode []byte, evalPath []byte, spool *slicepool.SlicePool) ([]byte, error) {
	var ret []byte
	err := util.CatchPanicOrError(func() error {
		var err1 error
		ret, err1 = tx._evalBytecode(bytecode, evalPath, spool)
		return err1
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// validateTxLevelConstraints evaluates all transaction level constraints, if any.
func (tx *Transaction) validateTxLevelConstraints(spool *slicepool.SlicePool) error {
	txConstraintsBytes := tx.MustBytesAtPath(ledger.PathToTxConstraints)
	if len(txConstraintsBytes) == 0 {
		// nil value of the txConstraints element is no-op
		return nil
	}
	tu, err := tuples.TupleFromBytes(txConstraintsBytes, 256)
	if err != nil {
		return fmt.Errorf("parsing tx constraints: %v", err)
	}
	// assume there a tuple of tx level constraints
	return tx.runTuple(tu, ledger.PathToTxConstraints, spool)
}

func (tx *Transaction) writeStateMutationsTo(mut common.KVWriter) {
	// delete consumed outputs from the ledger
	tx.ForEachInputID(func(idx byte, oid base.OutputID) bool {
		mut.Set(oid[:], nil)
		return true
	})
	// add produced outputs to the ledger
	tx.ForEachProducedOutputData(func(i byte, outputData []byte) bool {
		oid := base.MustNewOutputID(tx.txid, i)
		mut.Set(oid[:], outputData)
		return true
	})
}

// TODO optimize and improve:
//   - no need to pre-parse all outputs, we can run tuples directly
//   - we can check mandatory constraints separately

func (tx *Transaction) validateOutputs(spool *slicepool.SlicePool) error {
	outs, err := tx._scanOutputs(ledger.PathToConsumedOutputs)
	if err != nil {
		return err
	}
	if err = tx._sumConsumedTotals(outs); err != nil {
		return fmt.Errorf("validateOutputs: %w", err)
	}
	producedSide := tx.producedAmountTotals[ledger.AmountIndexTokenBalance]
	consumedSide := tx.totalConsumedTokenBalance
	inflation := tx.producedAmountTotals[ledger.AmountIndexInflation]
	if producedSide != consumedSide+inflation {
		return fmt.Errorf("mismatch between token amounts: consumed(%s) + inflation(%s) != produced(%s), diff c+i-p = %s",
			util.Th(consumedSide),
			util.Th(inflation),
			util.Th(producedSide),
			util.Th(consumedSide+inflation-producedSide),
		)
	}
	if err = tx._runOutputs(ledger.PathToConsumedOutputs, outs, spool); err != nil {
		return err
	}
	outs, err = tx._scanOutputs(ledger.PathToProducedOutputs)
	if err != nil {
		return err
	}
	if err = tx._runOutputs(ledger.PathToProducedOutputs, outs, spool); err != nil {
		return err
	}
	return nil
}

// _scanOutputs parses outputs using the transaction's cached library for deterministic validation.
// IMPORTANT: parsing does not depend on the library version
func (tx *Transaction) _scanOutputs(pathToOutputs []byte) ([]*ledger.Output, error) {
	var err error
	ret := make([]*ledger.Output, tx.MustNumElementsAtPath(pathToOutputs))

	_ = tx.ForEach(func(i byte, data []byte) bool {
		ret[i], err = ledger.OutputFromBytesWithLib(data, tx.Library) // TODO a bit redundant
		return err == nil
	}, pathToOutputs)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (tx *Transaction) _runOutputs(pathToOutputs []byte, outs []*ledger.Output, spool *slicepool.SlicePool) error {
	util.Assertf(len(outs) <= 256, "len(outs)<=256")

	path := common.Concat(pathToOutputs, 0)
	// reverse order of constraint validations -> to evaluate 'amounts' last
	// For valid UTXOs order how we run scripts does not matter.
	// For invalid UTXOs, order affects what error is detected first
	for i := len(outs) - 1; i >= 0; i-- {
		o := outs[i]
		var err error
		path[len(path)-1] = byte(i)
		if err = tx.runTuple(o.Tuple, path, spool); err != nil {
			return fmt.Errorf("%w :\n%s", err, o.LinesHR("   ").String())
		}
	}
	return nil
}

func (tx *Transaction) _sumConsumedTotals(outs []*ledger.Output) error {
	for i, o := range outs {
		bal := o.TokenBalance()
		if tx.totalConsumedTokenBalance > int64(math.MaxInt64)-int64(bal) {
			return fmt.Errorf("arithmetic overflow at consumed output #%d", i)
		}
		tx.totalConsumedTokenBalance += int64(bal)
	}
	return nil
}

func (tx *Transaction) UnlockParams(consumedOutputIdx, constraintIdx byte) []byte {
	return tx.MustBytesAtPath(easyfl_util.Concat(ledger.PathToUnlockParams, consumedOutputIdx, constraintIdx))
}

func (tx *Transaction) runTuple(tu *tuples.Tuple, ctxPath tuples.TreePath, spool *slicepool.SlicePool) error {
	evalPath := easyfl_util.Concat(ctxPath, byte(0))
	var err error

	// checking of script duplicates has been removed makes no sense?

	tu.ForEach(func(idx int, bytecode []byte) bool {
		evalPath[len(evalPath)-1] = byte(idx)
		var res []byte

		res, err = tx.evalBytecode(bytecode, evalPath, spool)
		if err != nil {
			err = fmt.Errorf("constraint '%s' failed with error '%v'. Path: %s", tx._constraintName(bytecode), err, PathToString(evalPath))
			return false
		}
		if len(res) == 0 {
			var decomp string
			decomp, err = tx.DecompileBytecode(bytecode)
			if err != nil {
				decomp = fmt.Sprintf("(error while decompiling constraint: '%v')", err)
			}
			err = fmt.Errorf("constraint '%s' failed. Path: %s", decomp, PathToString(evalPath))
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	return nil
}

func (tx *Transaction) SubtreeAtPath(path []byte) (*tuples.Tree, error) {
	return tx.Subtree(path)
}

func PathToString(path []byte) string {
	ret := "@"
	if len(path) == 0 {
		return ret + ".nil"
	}
	if len(path) >= 1 {
		switch path[0] {
		case ledger.TransactionTuple:
			ret += ".tx"
			if len(path) >= 2 {
				switch path[1] {
				case ledger.TxUnlockData:
					ret += ".unlock"
				case ledger.TxInputIDs:
					ret += ".inID"
				case ledger.TxOutputs:
					ret += ".out"
				case ledger.TxSignatureData:
					ret += ".sig"
				case ledger.TxTimestamp:
					ret += ".ts"
				case ledger.TxInputCommitment:
					ret += ".inhash"
				case ledger.TxConstraints:
					ret += ".txConstraints"
				case ledger.TxExplicitBaseline:
					ret += ".explicitBaseline"
				case ledger.TxEndorsements:
					ret += ".endorsements"
				case ledger.TxOtherData:
					ret += ".otherData"
				default:
					ret += "WRONG[1]"
				}
			}
			if len(path) >= 3 {
				ret += fmt.Sprintf("[%d]", path[2])
			}
			if len(path) >= 4 {
				ret += fmt.Sprintf(".constraint[%d]", path[3])
			}
			if len(path) >= 5 {
				ret += fmt.Sprintf("..%v", path[4:])
			}
		case ledger.ConsumedTuple:
			ret += ".consumed"
			if len(path) >= 2 {
				if path[1] != 0 {
					ret += ".WRONG[1]"
				} else {
					ret += ".[0]"
				}
			}
			if len(path) >= 3 {
				ret += fmt.Sprintf(".out[%d]", path[2])
			}
			if len(path) >= 4 {
				ret += fmt.Sprintf(".constraint[%d]", path[3])
			}
			if len(path) >= 5 {
				ret += fmt.Sprintf("..%v", path[4:])
			}
		default:
			ret += ".WRONG[0]"
		}
	}
	return ret
}

// _constraintName returns the name of the constraint from its bytecode using the transaction's cached library
func (tx *Transaction) _constraintName(binCode []byte) string {
	if binCode[0] == 0 {
		return "array_constraint"
	}
	prefix, err := tx.ParsePrefixBytecode(binCode)
	if err != nil {
		return fmt.Sprintf("unknown_constraint(%s)", easyfl_util.Fmt(binCode))
	}
	name, found := ledger.NameByPrefixWithLib(prefix, tx.Library)
	if found {
		return name
	}
	return fmt.Sprintf("constraint_call_prefix(%s)", easyfl_util.Fmt(prefix))
}

func (tx *Transaction) _evalBytecode(bytecode []byte, path []byte, spool *slicepool.SlicePool) ([]byte, error) {
	if len(bytecode) == 0 {
		return nil, fmt.Errorf("bytecode can't be empty")
	}
	var err error
	evalCtx := tx.makeEvalContext(path)
	if evalCtx.Trace() {
		evalCtx.PutTrace(fmt.Sprintf("--- check constraint '%s' at path %s", tx._constraintName(bytecode), PathToString(path)))
	}

	var ret []byte
	if bytecode[0] == 0 {
		return nil, fmt.Errorf("binary code cannot begin with 0-byte")
	}
	ret, err = tx.EvalFromBytecodeWithSlicePool(evalCtx, spool, bytecode)

	if evalCtx.Trace() {
		if err != nil {
			evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: FAILED with '%v'", tx._constraintName(bytecode), PathToString(path), err))
			printTraceIfEnabled(evalCtx)
		} else {
			if len(ret) == 0 {
				evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: FAILED", tx._constraintName(bytecode), PathToString(path)))
				printTraceIfEnabled(evalCtx)
			} else {
				evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: OK", tx._constraintName(bytecode), PathToString(path)))
			}
		}
	}

	return ret, err
}

// __printLogOnFail is a global var for controlling printing failed validation trace or not
var __printLogOnFail atomic.Bool

func printTraceIfEnabled(evalCtx easyfl.GlobalData[*ledger.EvalContext]) {
	if __printLogOnFail.Load() {
		evalCtx.(*easyfl.GlobalDataLog[*ledger.EvalContext]).PrintLog()
	}
}
