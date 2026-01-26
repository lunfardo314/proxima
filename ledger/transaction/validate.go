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
	// Use the transaction's cached library for validation
	ctx.dataContext.SetEvalPath(path)
	switch ctx.traceOption {
	case TraceOptionNone:
		return ctx.Library.NewGlobalDataNoTrace(ctx.dataContext)
	case TraceOptionAll:
		return ctx.Library.NewGlobalDataTracePrint(ctx.dataContext)
	case TraceOptionFailedConstraints:
		return ctx.Library.NewGlobalDataLog(ctx.dataContext)
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
	var err error

	spool := slicepool.New()
	defer spool.Dispose()

	err = util.CatchPanicOrError(func() error {
		var err1 error
		if err1 = ctx.validateOutputs(spool); err1 != nil {
			return err1
		}
		return nil
	})
	if err != nil {
		return err
	}
	if ctx.totalConsumedTokenBalance+ctx.totalProducedAmounts[1] != ctx.totalProducedAmounts[0] {
		return fmt.Errorf("unbalanced amount between inputs and outputs: cnsumed balance %s, produced balance %s, produced inflation: %s",
			util.Th(ctx.totalConsumedTokenBalance), util.Th(ctx.totalProducedAmounts[0]), util.Th(ctx.totalProducedAmounts[1]))
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

func (ctx *TxContext) validateOutputs(spool *slicepool.SlicePool) error {
	outs, err := ctx._scanOutputs(ledger.PathToConsumedOutputs)
	if err != nil {
		return err
	}
	if err = ctx._sumConsumedTotals(outs); err != nil {
		return fmt.Errorf("validateOutputs: %w", err)
	}
	producedSide := ctx.totalProducedAmounts[ledger.AmountIndexTokenBalance]
	consumedSide := int64(ctx.totalConsumedTokenBalance)
	inflation := ctx.totalProducedAmounts[ledger.AmountIndexInflation]
	if producedSide != consumedSide+inflation {
		return fmt.Errorf("mismatch between token amounts: consumed(%s) + inflation(%s) != produced(%s), diff c+i-p = %s",
			util.Th(consumedSide),
			util.Th(inflation),
			util.Th(producedSide),
			util.Th(consumedSide+inflation-producedSide),
		)
	}
	if err = ctx._runOutputs(ledger.PathToConsumedOutputs, outs, spool); err != nil {
		return err
	}
	outs, err = ctx._scanOutputs(ledger.PathToProducedOutputs)
	if err != nil {
		return err
	}
	if err = ctx._runOutputs(ledger.PathToProducedOutputs, outs, spool); err != nil {
		return err
	}
	return nil
}

// _scanOutputs parses outputs using the transaction's cached library for deterministic validation.
// All outputs (consumed and produced) are parsed with the same library version.
// IMPORTANT: Upgrade code is responsible for maintaining backward-compatible bytecode
// parsing to avoid non-determinism when consuming outputs created with older library versions.
func (ctx *TxContext) _scanOutputs(pathToOutputs []byte) ([]*ledger.Output, error) {
	var err error
	ret := make([]*ledger.Output, ctx.ctxTree.MustNumElementsAtPath(pathToOutputs))

	_ = ctx.ctxTree.ForEach(func(i byte, data []byte) bool {
		ret[i], err = ledger.OutputFromBytesWithLib(data, ctx.Transaction.Library)
		return err == nil
	}, pathToOutputs)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (ctx *TxContext) _runOutputs(pathToOutputs []byte, outs []*ledger.Output, spool *slicepool.SlicePool) error {
	util.Assertf(len(outs) <= 256, "len(outs)<=256")

	path := common.Concat(pathToOutputs, 0)
	// reverse order of constraint validations -> to evaluate 'amounts' last
	// For valid UTXOs order how we run scripts does not matter.
	// For invalid UTXOs, order affects what error is detected first
	for i := len(outs) - 1; i >= 0; i-- {
		o := outs[i]
		var err error
		path[len(path)-1] = byte(i)
		if err = ctx.runOutput(o, path, spool); err != nil {
			return fmt.Errorf("%w :\n%s", err, o.LinesHR("   ").String())
		}
	}
	return nil
}

func (ctx *TxContext) _sumConsumedTotals(outs []*ledger.Output) error {
	for i, o := range outs {
		bal := o.TokenBalance()
		if ctx.totalConsumedTokenBalance > int64(math.MaxInt64)-int64(bal) {
			return fmt.Errorf("arithmetic overflow at consumed output #%d", i)
		}
		ctx.totalConsumedTokenBalance += int64(bal)
	}
	return nil
}

func (ctx *TxContext) UnlockParams(consumedOutputIdx, constraintIdx byte) []byte {
	return ctx.ctxTree.MustBytesAtPath(Path(ledger.TransactionTuple, ledger.TxUnlockData, consumedOutputIdx, constraintIdx))
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
			err = fmt.Errorf("constraint '%s' failed with error '%v'. Path: %s", ctx.constraintName(bytecode), err, PathToString(evalPath))
			return false
		}
		if len(res) == 0 {
			var decomp string
			decomp, err = ctx.Library.DecompileBytecode(bytecode)
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
	consumedOutputBytes := ctx.ctxTree.MustBytesAtPath(Path(ledger.ConsumedTuple, ledger.ConsumedOutputsBranch))
	return blake2b.Sum256(consumedOutputBytes)
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

// constraintName returns the name of the constraint from its bytecode using the transaction's cached library
func (ctx *TxContext) constraintName(binCode []byte) string {
	if binCode[0] == 0 {
		return "array_constraint"
	}
	prefix, err := ctx.Library.ParsePrefixBytecode(binCode)
	if err != nil {
		return fmt.Sprintf("unknown_constraint(%s)", easyfl_util.Fmt(binCode))
	}
	name, found := ledger.NameByPrefixWithLib(prefix, ctx.Library)
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
		evalCtx.PutTrace(fmt.Sprintf("--- check constraint '%s' at path %s", ctx.constraintName(bytecode), PathToString(path)))
	}

	var ret []byte
	if bytecode[0] == 0 {
		return nil, fmt.Errorf("binary code cannot begin with 0-byte")
	}
	ret, err = ctx.Library.EvalFromBytecodeWithSlicePool(evalCtx, spool, bytecode)

	if evalCtx.Trace() {
		if err != nil {
			evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: FAILED with '%v'", ctx.constraintName(bytecode), PathToString(path), err))
			printTraceIfEnabled(evalCtx)
		} else {
			if len(ret) == 0 {
				evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: FAILED", ctx.constraintName(bytecode), PathToString(path)))
				printTraceIfEnabled(evalCtx)
			} else {
				evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: OK", ctx.constraintName(bytecode), PathToString(path)))
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
