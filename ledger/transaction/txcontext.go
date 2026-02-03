package transaction

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

// TxContext is a data structure, which contains transferable transaction, consumed outputs and constraint library
type TxContext struct {
	*Transaction
	ctxTree *tuples.Tree
	// calculated and cached values
	totalProducedAmounts      [15]int64
	totalConsumedTokenBalance int64
	dataContext               *ledger.EvalContext // EasyFL constraint validation context
}

var Path = tuples.Path

const (
	TraceOptionNone = iota
	TraceOptionAll
	TraceOptionFailedConstraints
)

// contextSkeleton creates transaction context that is missing consumed UTXOs part.
// The branch of consumed UTXOs in the context tree is replaced with the dummy empty tuple.
// This allows to run tx integrity validation scripts.
// Later, when inputs of the transaction becomes available (solidified), skeleton context can be upgraded to
// the full context with fullContextFromSkeleton
func (tx *Transaction) contextSkeleton() *TxContext {
	ret := &TxContext{
		Transaction:          tx,
		ctxTree:              nil,
		dataContext:          nil,
		totalProducedAmounts: tx.TotalProducedAmounts(),
	}
	e := tuples.MakeTupleFromSerializableElements(tuples.EmptyTupleEditable())
	ret.ctxTree = tuples.TreeFromTreesReadOnly(tx.Tree, e.AsTree()) // index 0 for transaction, index 1 for consumed outputs
	ret.dataContext = ledger.NewEvalContext(ret)
	return ret
}

func (tx *Transaction) SetFullContext(inputLoaderByIndex func(i byte) (*ledger.Output, error)) error {
	if !tx.ctx.IsSkeleton() {
		return nil
	}
	var err error
	tx.ctx, err = tx.ctx.fullContextFromSkeleton(inputLoaderByIndex)
	return err
}

// fullContextFromSkeleton full context is obtained from the skeleton
// by replacing dummy consumed UTXOs with the real ones
func (ctx *TxContext) fullContextFromSkeleton(inputLoaderByIndex func(i byte) (*ledger.Output, error)) (*TxContext, error) {
	consumedUTXOs := tuples.EmptyTupleEditable(256)
	n := ctx.NumInputs()
	for i := 0; i < n; i++ {
		o, err := inputLoaderByIndex(byte(i))
		if err != nil {
			return nil, fmt.Errorf("fullContextFromSkeleton: '%v'", err)
		}
		if o == nil {
			err = fmt.Errorf("fullContextFromSkeleton: cannot get consumed output at input index %d", i)
			return nil, err
		}
		consumedUTXOs.MustPush(o.Bytes())
	}
	ret := *ctx // shallow clone
	e := tuples.MakeTupleFromSerializableElements(consumedUTXOs)
	txTree, err := ctx.ctxTree.Subtree([]byte{ledger.TransactionTuple})
	util.AssertNoError(err, "ctx.Tree.Subtree([]byte{ledger.TransactionTuple})")

	ret.ctxTree = tuples.TreeFromTreesReadOnly(txTree, e.AsTree()) // index 0 for transaction, index 1 for consumed outputs
	ret.dataContext = ledger.NewEvalContext(&ret)
	return &ret, nil
}

// ContextFull creates full context from transaction
func (tx *Transaction) ContextFull(inputLoaderByIndex func(i byte) (*ledger.Output, error), traceOption ...int) (*TxContext, error) {
	return tx.contextSkeleton().fullContextFromSkeleton(inputLoaderByIndex)
}

// TxContextFromTransferableBytes constructs tuples.Tree from transaction bytes and consumed outputs
func TxContextFromTransferableBytes(txBytes []byte, fetchInput func(oid base.OutputID) ([]byte, bool), traceOption ...int) (*TxContext, error) {
	//tx, err := FromBytes(txBytes, ParseTotalProducedAmount, ParseSequencerData, ScanOutputs)
	tx, err := FromBytes(txBytes, ParseSequencerData, ScanOutputs)
	if err != nil {
		return nil, err
	}
	return tx.ContextFull(tx.InputLoaderByIndex(fetchInput), traceOption...)
}

func (ctx *TxContext) IsSkeleton() bool {
	return ctx.ctxTree.MustNumElementsAtPath(ledger.PathToConsumedOutputs) == 0
}

func (ctx *TxContext) BytesAtPath(path []byte) ([]byte, error) {
	return ctx.ctxTree.BytesAtPath(path)
}

func (ctx *TxContext) SubtreeAtPath(path []byte) (*tuples.Tree, error) {
	return ctx.ctxTree.Subtree(path)
}

// unlockScriptBinary finds the script from the data of unlock block
func (ctx *TxContext) unlockScriptBinary(invocationFullPath tuples.TreePath) []byte {
	unlockBlockPath := common.Concat(invocationFullPath)
	unlockBlockPath[1] = ledger.TxUnlockData
	return ctx.ctxTree.MustBytesAtPath(unlockBlockPath)
}

func (ctx *TxContext) rootContext() easyfl.GlobalData[*ledger.EvalContext] {
	return ctx.makeEvalContext(nil)
}

func (ctx *TxContext) TransactionBytes() []byte {
	return ctx.Transaction.Bytes()
}

func (ctx *TxContext) ForEachInputID(fun func(idx byte, oid *base.OutputID) bool) {
	ctx.Transaction.ForEachInput(func(i byte, oid base.OutputID) bool {
		return fun(i, &oid)
	})
}

func (ctx *TxContext) ForEachEndorsement(fun func(idx byte, txid *base.TransactionID) bool) {
	ctx.Transaction.ForEachEndorsement(func(idx byte, txid base.TransactionID) bool {
		return fun(idx, &txid)
	})
}

func (ctx *TxContext) ForEachProducedOutputData(fun func(idx byte, oData []byte) bool) {
	err := ctx.ctxTree.ForEach(func(i byte, outputData []byte) bool {
		return fun(i, outputData)
	}, ledger.PathToProducedOutputs)
	util.AssertNoError(err)
}

func (ctx *TxContext) ForEachProducedOutput(fun func(idx byte, out *ledger.Output, oid *base.OutputID) bool) {
	ctx.Transaction.ForEachProducedOutput(func(idx byte, out *ledger.Output, oid base.OutputID) bool {
		return fun(idx, out, &oid)
	})
}

func (ctx *TxContext) ForEachConsumedOutput(fun func(idx byte, oid *base.OutputID, out *ledger.Output) bool) {
	ctx.ForEachInputID(func(idx byte, oid *base.OutputID) bool {
		out, _ := ctx.ConsumedOutput(idx)
		if !fun(idx, oid, out) {
			return false
		}
		return true
	})
}

func (ctx *TxContext) ConsumedOutput(idx byte) (*ledger.Output, error) {
	data, err := ctx.ctxTree.BytesAtPath(Path(ledger.ConsumedTuple, ledger.ConsumedOutputsBranch, idx))
	if err != nil {
		return nil, err
	}
	// Use transaction's cached library for deterministic parsing.
	// IMPORTANT: Upgrade code is responsible for maintaining backward-compatible
	// bytecode parsing to avoid non-determinism when consuming outputs created
	// with older library versions.
	return ledger.OutputFromBytesWithLib(data, ctx.Library)
}

func (ctx *TxContext) ProducedOutputData(idx byte) []byte {
	return ctx.ctxTree.MustBytesAtPath(Path(ledger.TransactionTuple, ledger.TxOutputs, idx))
}

func (ctx *TxContext) InputID(idx byte) (base.OutputID, error) {
	return ctx.Transaction.InputAt(idx)
}

func (ctx *TxContext) MustInputID(idx byte) base.OutputID {
	ret, err := ctx.InputID(idx)
	util.AssertNoError(err)
	return ret
}

func (ctx *TxContext) MustTimestampData() ([]byte, base.LedgerTime) {
	ret := ctx.ctxTree.MustBytesAtPath(Path(ledger.TransactionTuple, ledger.TxTimestamp))
	retTs, err := base.LedgerTimeFromBytes(ret)
	util.AssertNoError(err)
	return ret, retTs
}

func (ctx *TxContext) ConsumedTotal(i byte) (ret int64) {
	if i == 0 {
		return ctx.totalConsumedTokenBalance
	}
	util.Assertf(int(i) < 15, "ConsumedTotal: wrong index %d", i)

	ctx.ForEachConsumedOutput(func(idx byte, oid *base.OutputID, out *ledger.Output) bool {
		ret += out.Amounts().Amount(i)
		return true
	})
	return
}

func (ctx *TxContext) ProducedTotal(i byte) int64 {
	util.Assertf(int(i) < len(ctx.totalProducedAmounts), "ProducedTotal: wrong index %d", i)
	return ctx.totalProducedAmounts[i]
}

func (ctx *TxContext) TotalInflation() uint64 {
	return uint64(ctx.totalProducedAmounts[ledger.AmountIndexInflation])
}

// TotalProducedAmounts returns produced amount totals up to the last non-zero
func (ctx *TxContext) TotalProducedAmounts() []int64 {
	lastNonZero := -1
	for i, a := range ctx.totalProducedAmounts {
		if a != 0 {
			lastNonZero = i
		}
	}
	return ctx.totalProducedAmounts[:lastNonZero+1]
}

func (ctx *TxContext) Tx() *Transaction {
	return ctx.Transaction
}

func (ctx *TxContext) SpenderID() (base.SpenderID, error) {
	sig, err := ctx.Transaction.Signature()
	if err != nil {
		return [32]byte{}, err
	}
	return sig.SpenderID(), nil
}
