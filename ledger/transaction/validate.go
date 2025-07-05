package transaction

import (
	"bytes"
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
	"golang.org/x/crypto/blake2b"
)

func (ctx *TxContext) makeEvalContext(path []byte) easyfl.GlobalData[*ledger.EvalContext] {
	ctx.dataContext.SetEvalPath(path)
	switch ctx.traceOption {
	case TraceOptionNone:
		return ledger.L().NewGlobalDataNoTrace(ctx.dataContext)
	case TraceOptionAll:
		return ledger.L().NewGlobalDataTracePrint(ctx.dataContext)
	case TraceOptionFailedConstraints:
		return ledger.L().NewGlobalDataLog(ctx.dataContext)
	default:
		panic("wrong trace option")
	}
}

// EvalBytecode safely runs the bytecode in the context of the path
func (ctx *TxContext) EvalBytecode(bytecode []byte, evalPath []byte, spool *slicepool.SlicePool) ([]byte, error) {
	var ret []byte
	err := util.CatchPanicOrError(func() error {
		var err1 error
		ret, err1 = ctx._evalBytecode(bytecode, evalPath, spool)
		return err1
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (ctx *TxContext) Validate() error {
	if err := ctx._validate(); err != nil {
		return fmt.Errorf("%w\ntxid = %s (%s)", err, ctx.txid.StringShort(), ctx.txid.StringHex())
	}
	return nil
}

// _validate runs scripts on consumed and produced parts. Does not check the consistency of input commitment, because
// it already checked upon creation of the transaction context
func (ctx *TxContext) _validate() error {
	var inSum, outSum uint64
	var err error

	spool := slicepool.New()
	defer spool.Dispose()

	err = util.CatchPanicOrError(func() error {
		var err1 error
		if inSum, err1 = ctx.validateOutputsFailFast(true, spool); err1 != nil {
			return err1
		}
		if outSum, err1 = ctx.validateOutputsFailFast(false, spool); err1 != nil {
			return err1
		}
		return nil
	})
	if err != nil {
		return err
	}
	if inSum+ctx.inflationAmount != outSum {
		return fmt.Errorf("unbalanced amount between inputs and outputs: inputs %s, outputs %s, inflation: %s",
			util.Th(inSum), util.Th(outSum), util.Th(ctx.inflationAmount))
	}
	return nil
}

func (ctx *TxContext) writeStateMutationsTo(mut common.KVWriter) {
	// delete consumed outputs from the ledger
	ctx.ForEachInputID(func(idx byte, oid *base.OutputID) bool {
		mut.Set(oid[:], nil)
		return true
	})
	// add produced outputs to the ledger

	ctx.ForEachProducedOutputData(func(i byte, outputData []byte) bool {
		oid := base.MustNewOutputID(ctx.txid, i)
		mut.Set(oid[:], outputData)
		return true
	})
}

// ValidateWithReportOnConsumedOutputs validates the transaction and returns indices of failing consumed outputs, if any
// This for the convenience of automated VMs and sequencers
//func (ctx *TxContext) ValidateWithReportOnConsumedOutputs() ([]byte, error) {
//	var inSum, outSum uint64
//	var err error
//	var retFailedConsumed []byte
//
//	spool := slicepool.New()
//	defer spool.Dispose()
//
//	err = util.CatchPanicOrError(func() error {
//		var err1 error
//		inSum, retFailedConsumed, err1 = ctx._validateOutputs(true, false, spool)
//		return err1
//	})
//	if err != nil {
//		// return list of failed consumed outputs
//		return retFailedConsumed, err
//	}
//	err = util.CatchPanicOrError(func() error {
//		var err1 error
//		outSum, _, err1 = ctx._validateOutputs(false, true, spool)
//		return err1
//	})
//	if err != nil {
//		return nil, err
//	}
//	err = util.CatchPanicOrError(func() error {
//		return ctx.validateInputCommitmentSafe()
//	})
//	if err != nil {
//		return nil, err
//	}
//
//	if inSum+ctx.inflationAmount != outSum {
//		return nil, fmt.Errorf("unbalanced amount between inputs and outputs: inputs %s + inflation: %s != outputs %s",
//			util.Th(inSum), util.Th(ctx.inflationAmount), util.Th(outSum))
//	}
//	return nil, nil
//}

func (ctx *TxContext) validateOutputsFailFast(consumedBranch bool, spool *slicepool.SlicePool) (uint64, error) {
	totalAmount, _, err := ctx._validateOutputs(consumedBranch, true, spool)
	return totalAmount, err
}

// _validateOutputs validates consumed or produced outputs and, optionally, either fails fast,
// or return the list of indices of failed outputs
// If err != nil and failFast = false, returns list of failed consumed and produced output respectively
// if failFast = true, returns (totalAmount, nil, nil, error)
func (ctx *TxContext) _validateOutputs(consumedBranch bool, failFast bool, spool *slicepool.SlicePool) (uint64, []byte, error) {
	var branch tuples.TreePath
	if consumedBranch {
		branch = Path(ledger.ConsumedBranch, ledger.ConsumedOutputsBranch)
	} else {
		branch = Path(ledger.TransactionBranch, ledger.TxOutputs)
	}
	var lastErr error
	var sum uint64
	var failedOutputs bytes.Buffer

	path := common.Concat(branch, 0)
	_ = ctx.tree.ForEach(func(i byte, data []byte) bool {
		var err error
		path[len(path)-1] = i
		o, err := ledger.OutputFromBytesReadOnly(data)
		if err != nil {
			if !failFast {
				failedOutputs.WriteByte(i)
			}
			lastErr = err
			return !failFast
		}

		if err = ctx.runOutput(o, path, spool); err != nil {
			if !failFast {
				failedOutputs.WriteByte(i)
			}
			lastErr = fmt.Errorf("%w :\n%s", err, o.ToSource("   "))
			return !failFast
		}
		minDeposit := o.MinimumStorageDeposit(0)
		amount := o.TokenBalance()
		if amount < minDeposit {
			lastErr = fmt.Errorf("not enough storage deposit in output %s. Minimum %d, got %d",
				PathToString(path), minDeposit, amount)
			if !failFast {
				failedOutputs.WriteByte(i)
			}
			return !failFast
		}
		if amount > math.MaxUint64-sum {
			lastErr = fmt.Errorf("validateOutputsFailFast @ path %s: uint64 arithmetic overflow", PathToString(path))
			if !failFast {
				failedOutputs.WriteByte(i)
			}
			return !failFast
		}
		sum += amount
		return true
	}, branch)
	if lastErr != nil {
		util.Assertf(failFast || failedOutputs.Len() > 0, "failedOutputs.Len()>0")
		return 0, failedOutputs.Bytes(), lastErr
	}
	return sum, nil, nil
}

func (ctx *TxContext) UnlockParams(consumedOutputIdx, constraintIdx byte) []byte {
	return ctx.tree.MustBytesAtPath(Path(ledger.TransactionBranch, ledger.TxUnlockData, consumedOutputIdx, constraintIdx))
}

// runOutput checks constraints of the output one-by-one
func (ctx *TxContext) runOutput(output *ledger.Output, path tuples.TreePath, spool *slicepool.SlicePool) error {
	evalPath := common.Concat(path, byte(0))
	var err error

	// checking of script duplicates has been removed. Makes no sense

	output.ForEachConstraint(func(idx byte, bytecode []byte) bool {
		evalPath[len(evalPath)-1] = idx
		var res []byte

		res, err = ctx.EvalBytecode(bytecode, evalPath, spool)
		if err != nil {
			err = fmt.Errorf("constraint '%s' failed with error '%v'. Path: %s", constraintName(bytecode), err, PathToString(evalPath))
			return false
		}
		if len(res) == 0 {
			var decomp string
			decomp, err = ledger.L().DecompileBytecode(bytecode)
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

func (ctx *TxContext) validateInputCommitmentSafe() error {
	return util.CatchPanicOrError(func() error {
		consumeOutputHash := ctx.ConsumedOutputHash()
		inputCommitment := ctx.InputCommitment()
		if !bytes.Equal(consumeOutputHash[:], inputCommitment) {
			return fmt.Errorf("hash of consumed outputs %v not equal to input commitment %v",
				easyfl_util.Fmt(consumeOutputHash[:]), easyfl_util.Fmt(inputCommitment))
		}
		return nil
	})
}

// ConsumedOutputHash is ias blake2b hash of the tuple composed of output data
func (ctx *TxContext) ConsumedOutputHash() [32]byte {
	consumedOutputBytes := ctx.tree.MustBytesAtPath(Path(ledger.ConsumedBranch, ledger.ConsumedOutputsBranch))
	return blake2b.Sum256(consumedOutputBytes)
}

func PathToString(path []byte) string {
	ret := "@"
	if len(path) == 0 {
		return ret + ".nil"
	}
	if len(path) >= 1 {
		switch path[0] {
		case ledger.TransactionBranch:
			ret += ".tx"
			if len(path) >= 2 {
				switch path[1] {
				case ledger.TxUnlockData:
					ret += ".unlock"
				case ledger.TxInputIDs:
					ret += ".inID"
				case ledger.TxOutputs:
					ret += ".out"
				case ledger.TxSignature:
					ret += ".sig"
				case ledger.TxTimestamp:
					ret += ".ts"
				case ledger.TxInputCommitment:
					ret += ".inhash"
				default:
					ret += "WRONG[1]"
				}
			}
			if len(path) >= 3 {
				ret += fmt.Sprintf("[%d]", path[2])
			}
			if len(path) >= 4 {
				ret += fmt.Sprintf(".block[%d]", path[3])
			}
			if len(path) >= 5 {
				ret += fmt.Sprintf("..%v", path[4:])
			}
		case ledger.ConsumedBranch:
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
				ret += fmt.Sprintf(".block[%d]", path[3])
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

func constraintName(binCode []byte) string {
	if binCode[0] == 0 {
		return "array_constraint"
	}
	prefix, err := ledger.L().ParsePrefixBytecode(binCode)
	if err != nil {
		return fmt.Sprintf("unknown_constraint(%s)", easyfl_util.Fmt(binCode))
	}
	name, found := ledger.NameByPrefix(prefix)
	if found {
		return name
	}
	return fmt.Sprintf("constraint_call_prefix(%s)", easyfl_util.Fmt(prefix))
}

func (ctx *TxContext) _evalBytecode(bytecode []byte, path []byte, spool *slicepool.SlicePool) ([]byte, error) {
	if len(bytecode) == 0 {
		return nil, fmt.Errorf("constraint can't be empty")
	}
	var err error
	evalCtx := ctx.makeEvalContext(path)
	if evalCtx.Trace() {
		evalCtx.PutTrace(fmt.Sprintf("--- check constraint '%s' at path %s", constraintName(bytecode), PathToString(path)))
	}

	var ret []byte
	if bytecode[0] == 0 {
		return nil, fmt.Errorf("binary code cannot begin with 0-byte")
	}
	ret, err = ledger.L().EvalFromBytecodeWithSlicePool(evalCtx, spool, bytecode)

	if evalCtx.Trace() {
		if err != nil {
			evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: FAILED with '%v'", constraintName(bytecode), PathToString(path), err))
			printTraceIfEnabled(evalCtx)
		} else {
			if len(ret) == 0 {
				evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: FAILED", constraintName(bytecode), PathToString(path)))
				printTraceIfEnabled(evalCtx)
			} else {
				evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: OK", constraintName(bytecode), PathToString(path)))
			}
		}
	}

	return ret, err
}

// __printLogOnFail is global var for controlling printing failed validation trace or not
var __printLogOnFail atomic.Bool

func printTraceIfEnabled(evalCtx easyfl.GlobalData[*ledger.EvalContext]) {
	if __printLogOnFail.Load() {
		evalCtx.(*easyfl.GlobalDataLog[*ledger.EvalContext]).PrintLog()
	}
}
