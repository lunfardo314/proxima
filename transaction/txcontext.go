package transaction

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyslice"
	"github.com/lunfardo314/unitrie/common"
)

// TransactionContext is a data structure, which contains transferable transaction, consumed outputs and constraint library
type TransactionContext struct {
	tree        *lazyslice.Tree
	traceOption int
	// cached values
	dataContext *core.DataContext
	txid        *core.TransactionID
	sender      core.AddressED25519
}

var Path = lazyslice.Path

const (
	TraceOptionNone = iota
	TraceOptionAll
	TraceOptionFailedConstraints
)

func ContextFromTransaction(tx *Transaction, inputLoaderByIndex func(i byte) (*core.Output, error), traceOption ...int) (*TransactionContext, error) {
	ret := &TransactionContext{
		tree:        nil,
		traceOption: TraceOptionNone,
		dataContext: nil,
		txid:        tx.ID(),
		sender:      tx.SenderAddress(),
	}
	if len(traceOption) > 0 {
		ret.traceOption = traceOption[0]
	}
	consumedOutputsArray := lazyslice.EmptyArray(256)
	for i := 0; i < tx.NumInputs(); i++ {
		o, err := inputLoaderByIndex(byte(i))
		if err != nil {
			return nil, fmt.Errorf("ContextFromTransaction: '%v'", err)
		}
		if o == nil {
			return nil, fmt.Errorf("ContextFromTransaction: input not solid at index %d", i)
		}
		consumedOutputsArray.Push(o.Bytes())
	}
	e := lazyslice.MakeArrayReadOnly(consumedOutputsArray) // one level deeper
	ret.tree = lazyslice.TreeFromTreesReadOnly(tx.tree, e.AsTree())
	ret.dataContext = core.NewDataContext(ret.tree)
	return ret, nil
}

// ContextFromTransferableBytes constructs lazytree from transaction bytes and consumed outputs
func ContextFromTransferableBytes(txBytes []byte, fetchInput func(oid *core.OutputID) ([]byte, bool), traceOption ...int) (*TransactionContext, error) {
	tx, err := TransactionFromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	return ContextFromTransaction(tx, tx.InputLoaderByIndex(fetchInput), traceOption...)
}

// unlockScriptBinary finds script from unlock block
func (ctx *TransactionContext) unlockScriptBinary(invocationFullPath lazyslice.TreePath) []byte {
	unlockBlockPath := common.Concat(invocationFullPath)
	unlockBlockPath[1] = core.TxUnlockParams
	return ctx.tree.BytesAtPath(unlockBlockPath)
}

func (ctx *TransactionContext) rootContext() easyfl.GlobalData {
	return ctx.evalContext(nil)
}

func (ctx *TransactionContext) TransactionBytes() []byte {
	return ctx.tree.BytesAtPath(Path(core.TransactionBranch))
}

func (ctx *TransactionContext) TransactionID() *core.TransactionID {
	return ctx.txid
}

func (ctx *TransactionContext) InputCommitment() []byte {
	return ctx.tree.BytesAtPath(Path(core.TransactionBranch, core.TxInputCommitment))
}

func (ctx *TransactionContext) Signature() []byte {
	return ctx.tree.BytesAtPath(Path(core.TransactionBranch, core.TxSignature))
}

func (ctx *TransactionContext) ForEachInputID(fun func(idx byte, oid *core.OutputID) bool) {
	ctx.tree.ForEach(func(i byte, data []byte) bool {
		oid, err := core.OutputIDFromBytes(data)
		util.AssertNoError(err)
		if !fun(i, &oid) {
			return false
		}
		return true
	}, Path(core.TransactionBranch, core.TxInputIDs))
}

func (ctx *TransactionContext) ForEachEndorsement(fun func(idx byte, txid *core.TransactionID) bool) {
	ctx.tree.ForEach(func(i byte, data []byte) bool {
		txid, err := core.TransactionIDFromBytes(data)
		util.AssertNoError(err)
		if !fun(i, &txid) {
			return false
		}
		return true
	}, Path(core.TransactionBranch, core.TxEndorsements))
}

func (ctx *TransactionContext) ForEachProducedOutputData(fun func(idx byte, oData []byte) bool) {
	ctx.tree.ForEach(func(i byte, outputData []byte) bool {
		return fun(i, outputData)
	}, core.PathToProducedOutputs)
}

func (ctx *TransactionContext) ForEachProducedOutput(fun func(idx byte, out *core.Output, oid *core.OutputID) bool) {
	ctx.ForEachProducedOutputData(func(idx byte, oData []byte) bool {
		out, _ := core.OutputFromBytesReadOnly(oData)
		oid := ctx.OutputID(idx)
		if !fun(idx, out, &oid) {
			return false
		}
		return true
	})
}

func (ctx *TransactionContext) ForEachConsumedOutput(fun func(idx byte, oid *core.OutputID, out *core.Output) bool) {
	ctx.ForEachInputID(func(idx byte, oid *core.OutputID) bool {
		out, _ := ctx.ConsumedOutput(idx)
		if !fun(idx, oid, out) {
			return false
		}
		return true
	})
}

func (ctx *TransactionContext) ConsumedOutputData(idx byte) []byte {
	return ctx.tree.BytesAtPath(Path(core.ConsumedBranch, core.ConsumedOutputsBranch, idx))
}

func (ctx *TransactionContext) ConsumedOutput(idx byte) (*core.Output, error) {
	return core.OutputFromBytesReadOnly(ctx.ConsumedOutputData(idx))
}

func (ctx *TransactionContext) UnlockData(idx byte) []byte {
	return ctx.tree.BytesAtPath(Path(core.TransactionBranch, core.TxUnlockParams, idx))
}

func (ctx *TransactionContext) ProducedOutputData(idx byte) []byte {
	return ctx.tree.BytesAtPath(Path(core.TransactionBranch, core.TxOutputs, idx))
}

func (ctx *TransactionContext) NumProducedOutputs() int {
	return ctx.tree.NumElements([]byte{core.TransactionBranch, core.TxOutputs})
}

func (ctx *TransactionContext) NumInputs() int {
	return ctx.tree.NumElements([]byte{core.TransactionBranch, core.TxInputIDs})
}

func (ctx *TransactionContext) NumEndorsements() int {
	return ctx.tree.NumElements([]byte{core.TransactionBranch, core.TxEndorsements})
}

func (ctx *TransactionContext) InputID(idx byte) core.OutputID {
	data := ctx.tree.BytesAtPath(Path(core.TransactionBranch, core.TxInputIDs, idx))
	ret, err := core.OutputIDFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func (ctx *TransactionContext) MustTimestampData() ([]byte, core.LogicalTime) {
	ret := ctx.tree.BytesAtPath(Path(core.TransactionBranch, core.TxTimestamp))
	retTs, err := core.LogicalTimeFromBytes(ret)
	util.AssertNoError(err)
	return ret, retTs
}

func (ctx *TransactionContext) SequencerAndStemOutputIndices() (byte, byte) {
	ret := ctx.tree.BytesAtPath(core.PathToSequencerAndStemOutputIndices)
	util.Assertf(len(ret) == 2, "len(ret)==2")
	return ret[0], ret[1]
}

func (ctx *TransactionContext) OutputID(idx byte) core.OutputID {
	return core.NewOutputID(ctx.txid, idx)
}
