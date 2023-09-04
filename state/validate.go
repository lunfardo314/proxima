package state

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyslice"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/atomic"
	"golang.org/x/crypto/blake2b"
)

func (ctx *TransactionContext) evalContext(path []byte) easyfl.GlobalData {
	ctx.dataContext.SetPath(path)
	switch ctx.traceOption {
	case TraceOptionNone:
		return easyfl.NewGlobalDataNoTrace(ctx.dataContext)
	case TraceOptionAll:
		return easyfl.NewGlobalDataTracePrint(ctx.dataContext)
	case TraceOptionFailedConstraints:
		return easyfl.NewGlobalDataLog(ctx.dataContext)
	default:
		panic("wrong trace option")
	}
}

// checkConstraint checks the constraint at path. In-line and unlock scripts are ignored
// for 'produces output' context
func (ctx *TransactionContext) checkConstraint(constraintData []byte, constraintPath lazyslice.TreePath) ([]byte, string, error) {
	var ret []byte
	var name string
	err := util.CatchPanicOrError(func() error {
		var err1 error
		ret, name, err1 = ctx.evalConstraint(constraintData, constraintPath)
		return err1
	})
	if err != nil {
		return nil, name, err
	}
	return ret, name, nil
}

func (ctx *TransactionContext) Validate() error {
	var inSum, outSum uint64
	var err error

	err = util.CatchPanicOrError(func() error {
		var err1 error
		inSum, err1 = ctx.validateOutputsFailFast(true)
		return err1
	})
	if err != nil {
		return err
	}
	err = util.CatchPanicOrError(func() error {
		var err1 error
		outSum, err1 = ctx.validateOutputsFailFast(false)
		return err1
	})
	if err != nil {
		return err
	}
	err = util.CatchPanicOrError(func() error {
		return ctx.validateInputCommitment()
	})
	if err != nil {
		return err
	}
	if inSum != outSum {
		return fmt.Errorf("unbalanced amount between inputs and outputs: inputs %s, outputs %s",
			testutil.GoThousands(inSum), testutil.GoThousands(outSum))
	}
	return nil
}

func (ctx *TransactionContext) writeStateMutationsTo(mut common.KVWriter) {
	// delete consumed outputs from the ledger
	ctx.ForEachInputID(func(idx byte, oid *core.OutputID) bool {
		mut.Set(oid[:], nil)
		return true
	})
	// add produced outputs to the ledger

	ctx.ForEachProducedOutputData(func(i byte, outputData []byte) bool {
		oid := core.NewOutputID(ctx.txid, i)
		mut.Set(oid[:], outputData)
		return true
	})
}

// ValidateWithReportOnConsumedOutputs validates the transaction and returns indices of failing consumed outputs, if any
// This for the convenience of automated VMs and sequencers
func (ctx *TransactionContext) ValidateWithReportOnConsumedOutputs() ([]byte, error) {
	var inSum, outSum uint64
	var err error
	var retFailedConsumed []byte

	err = util.CatchPanicOrError(func() error {
		var err1 error
		inSum, retFailedConsumed, err1 = ctx._validateOutputs(true, false)
		return err1
	})
	if err != nil {
		// return list of failed consumed outputs
		return retFailedConsumed, err
	}
	err = util.CatchPanicOrError(func() error {
		var err1 error
		outSum, _, err1 = ctx._validateOutputs(false, true)
		return err1
	})
	if err != nil {
		return nil, err
	}
	err = util.CatchPanicOrError(func() error {
		return ctx.validateInputCommitment()
	})
	if err != nil {
		return nil, err
	}
	if inSum != outSum {
		return nil, fmt.Errorf("unbalanced amount between inputs and outputs: inputs %s, outputs %s",
			testutil.GoThousands(inSum), testutil.GoThousands(outSum))
	}
	return nil, nil
}

func (ctx *TransactionContext) validateOutputsFailFast(consumedBranch bool) (uint64, error) {
	totalAmount, _, err := ctx._validateOutputs(consumedBranch, true)
	return totalAmount, err
}

// _validateOutputs validates consumed or produced outputs and, optionally, either fails fast,
// or return the list of indices of failed outputs
// If err != nil and failFast = false, returns list of failed consumed and produced output respectively
// if failFast = true, returns (totalAmount, nil, nil, error)
func (ctx *TransactionContext) _validateOutputs(consumedBranch bool, failFast bool) (uint64, []byte, error) {
	var branch lazyslice.TreePath
	if consumedBranch {
		branch = Path(core.ConsumedBranch, core.ConsumedOutputsBranch)
	} else {
		branch = Path(core.TransactionBranch, core.TxOutputs)
	}
	var lastErr error
	var sum uint64
	var extraDepositWeight uint32
	var failedOutputs bytes.Buffer

	path := common.Concat(branch, 0)
	ctx.tree.ForEach(func(i byte, data []byte) bool {
		var err error
		path[len(path)-1] = i
		o, err := core.OutputFromBytesReadOnly(data)
		if err != nil {
			if !failFast {
				failedOutputs.WriteByte(i)
			}
			lastErr = err
			return !failFast
		}

		if extraDepositWeight, err = ctx.runOutput(consumedBranch, o, path); err != nil {
			if !failFast {
				failedOutputs.WriteByte(i)
			}
			lastErr = fmt.Errorf("%v :\n%s", err, o.ToString("   "))
			return !failFast
		}
		minDeposit := core.MinimumStorageDeposit(o, extraDepositWeight)
		amount := o.Amount()
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

func (ctx *TransactionContext) UnlockParams(consumedOutputIdx, constraintIdx byte) []byte {
	return ctx.tree.BytesAtPath(Path(core.TransactionBranch, core.TxUnlockParams, consumedOutputIdx, constraintIdx))
}

// runOutput checks constraints of the output one-by-one
func (ctx *TransactionContext) runOutput(consumedBranch bool, output *core.Output, path lazyslice.TreePath) (uint32, error) {
	blockPath := common.Concat(path, byte(0))
	var err error
	extraStorageDepositWeight := uint32(0)
	checkDuplicates := make(map[string]struct{})

	output.ForEachConstraint(func(idx byte, data []byte) bool {
		// checking for duplicated constraints in produced outputs
		if !consumedBranch {
			sd := string(data)
			if _, already := checkDuplicates[sd]; already {
				err = fmt.Errorf("duplicated constraints not allowed. Path %s", PathToString(blockPath))
				return false
			}
			checkDuplicates[sd] = struct{}{}
		}
		blockPath[len(blockPath)-1] = byte(idx)
		var res []byte
		var name string

		res, name, err = ctx.checkConstraint(data, blockPath)
		if err != nil {
			err = fmt.Errorf("constraint '%s' failed with error '%v'. Path: %s",
				name, err, PathToString(blockPath))
			return false
		}
		if len(res) == 0 {
			var decomp string
			decomp, err = easyfl.DecompileBytecode(data)
			if err != nil {
				decomp = fmt.Sprintf("(error while decompiling constraint: '%v')", err)
			}
			err = fmt.Errorf("constraint '%s' failed. Path: %s", decomp, PathToString(blockPath))
			return false
		}
		if len(res) == 4 {
			// 4 bytes long slice returned by the constraint is interpreted as 'true' and as uint32 extraStorageWeight
			extraStorageDepositWeight += binary.BigEndian.Uint32(res)
		}
		return true
	})
	if err != nil {
		return 0, err
	}
	return extraStorageDepositWeight, nil
}

func (ctx *TransactionContext) validateInputCommitment() error {
	consumeOutputHash := ctx.ConsumedOutputHash()
	inputCommitment := ctx.InputCommitment()
	if !bytes.Equal(consumeOutputHash[:], inputCommitment) {
		return fmt.Errorf("hash of consumed outputs %v not equal to input commitment %v",
			easyfl.Fmt(consumeOutputHash[:]), easyfl.Fmt(inputCommitment))
	}
	return nil
}

func (ctx *TransactionContext) ConsumedOutputHash() [32]byte {
	consumedOutputBytes := ctx.tree.BytesAtPath(Path(core.ConsumedBranch, core.ConsumedOutputsBranch))
	return blake2b.Sum256(consumedOutputBytes)
}

func PathToString(path []byte) string {
	ret := "@"
	if len(path) == 0 {
		return ret + ".nil"
	}
	if len(path) >= 1 {
		switch path[0] {
		case core.TransactionBranch:
			ret += ".tx"
			if len(path) >= 2 {
				switch path[1] {
				case core.TxUnlockParams:
					ret += ".unlock"
				case core.TxInputIDs:
					ret += ".inID"
				case core.TxOutputs:
					ret += ".out"
				case core.TxSignature:
					ret += ".sig"
				case core.TxTimestamp:
					ret += ".ts"
				case core.TxInputCommitment:
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
		case core.ConsumedBranch:
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
	prefix, err := easyfl.ParseBytecodePrefix(binCode)
	if err != nil {
		return fmt.Sprintf("unknown_constraint(%s)", easyfl.Fmt(binCode))
	}
	name, found := core.NameByPrefix(prefix)
	if found {
		return name
	}
	return fmt.Sprintf("constraint_call_prefix(%s)", easyfl.Fmt(prefix))
}

func (ctx *TransactionContext) evalConstraint(constr []byte, path lazyslice.TreePath) ([]byte, string, error) {
	if len(constr) == 0 {
		return nil, "", fmt.Errorf("constraint can't be empty")
	}
	var err error
	name := constraintName(constr)
	evalCtx := ctx.evalContext(path)
	if evalCtx.Trace() {
		evalCtx.PutTrace(fmt.Sprintf("--- check constraint '%s' at path %s", name, PathToString(path)))
	}

	var ret []byte

	if constr[0] != 0 {
		// inline constraint. Binary code cannot begin with 0-byte
		ret, err = easyfl.EvalFromBinary(evalCtx, constr)
	} else {
		// array constraint TODO do we need it?
		arr := lazyslice.ArrayFromBytesReadOnly(constr[1:], 256)
		if arr.NumElements() == 0 {
			err = fmt.Errorf("can't evaluate empty array")
		} else {
			binCode := arr.At(0)
			args := make([][]byte, arr.NumElements()-1)
			for i := 1; i < arr.NumElements(); i++ {
				args[i] = arr.At(i)
			}
			ret, err = easyfl.EvalFromBinary(evalCtx, binCode, args...)
		}
	}

	if evalCtx.Trace() {
		if err != nil {
			evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: FAILED with '%v'", name, PathToString(path), err))
			printTraceIfEnabled(evalCtx)
		} else {
			if len(ret) == 0 {
				evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: FAILED", name, PathToString(path)))
				printTraceIfEnabled(evalCtx)
			} else {
				evalCtx.PutTrace(fmt.Sprintf("--- constraint '%s' at path %s: OK", name, PathToString(path)))
			}
		}
	}

	return ret, name, err
}

// __printLogOnFail is global var for controlling printing failed validation trace or not
var __printLogOnFail atomic.Bool

func printTraceIfEnabled(evalCtx easyfl.GlobalData) {
	if __printLogOnFail.Load() {
		evalCtx.(*easyfl.GlobalDataLog).PrintLog()
	}
}

func SetPrintEasyFLTraceOnFail(v bool) {
	__printLogOnFail.Store(v)
}
