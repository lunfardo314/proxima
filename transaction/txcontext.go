package transaction

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/unitrie/common"
)

// TransactionContext is a data structure, which contains transferable transaction, consumed outputs and constraint library
type TransactionContext struct {
	tree        *lazybytes.Tree
	traceOption int
	// cached values
	dataContext     *ledger.DataContext
	txid            *ledger.TransactionID
	sender          ledger.AddressED25519
	inflationAmount uint64
}

var Path = lazybytes.Path

const (
	TraceOptionNone = iota
	TraceOptionAll
	TraceOptionFailedConstraints
)

func ContextFromTransaction(tx *Transaction, inputLoaderByIndex func(i byte) (*ledger.Output, error), traceOption ...int) (*TransactionContext, error) {
	ret := &TransactionContext{
		tree:            nil,
		traceOption:     TraceOptionNone,
		dataContext:     nil,
		txid:            tx.ID(),
		sender:          tx.SenderAddress(),
		inflationAmount: tx.InflationAmount(),
	}
	if len(traceOption) > 0 {
		ret.traceOption = traceOption[0]
	}
	consumedOutputsArray := lazybytes.EmptyArray(256)
	for i := 0; i < tx.NumInputs(); i++ {
		o, err := inputLoaderByIndex(byte(i))
		if err != nil {
			return nil, fmt.Errorf("ContextFromTransaction: '%v'", err)
		}
		if o == nil {
			inpOid := tx.MustInputAt(byte(i))
			err = fmt.Errorf("ContextFromTransaction: cannot get consumed output %s at input index %d of %s",
				inpOid.StringShort(), i, tx.IDShortString())
			return nil, err
		}
		consumedOutputsArray.Push(o.Bytes())
	}
	e := lazybytes.MakeArrayReadOnly(consumedOutputsArray) // one level deeper
	ret.tree = lazybytes.TreeFromTreesReadOnly(tx.tree, e.AsTree())
	ret.dataContext = ledger.NewDataContext(ret.tree)
	return ret, nil
}

// ContextFromTransferableBytes constructs lazybytes.Tree from transaction bytes and consumed outputs
func ContextFromTransferableBytes(txBytes []byte, fetchInput func(oid *ledger.OutputID) ([]byte, bool), traceOption ...int) (*TransactionContext, error) {
	tx, err := FromBytes(txBytes, ScanSequencerData())
	if err != nil {
		return nil, err
	}
	return ContextFromTransaction(tx, tx.InputLoaderByIndex(fetchInput), traceOption...)
}

// unlockScriptBinary finds script from unlock block
func (ctx *TransactionContext) unlockScriptBinary(invocationFullPath lazybytes.TreePath) []byte {
	unlockBlockPath := common.Concat(invocationFullPath)
	unlockBlockPath[1] = ledger.TxUnlockParams
	return ctx.tree.BytesAtPath(unlockBlockPath)
}

func (ctx *TransactionContext) rootContext() easyfl.GlobalData {
	return ctx.evalContext(nil)
}

func (ctx *TransactionContext) TransactionBytes() []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch))
}

func (ctx *TransactionContext) TransactionID() *ledger.TransactionID {
	return ctx.txid
}

func (ctx *TransactionContext) InputCommitment() []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxInputCommitment))
}

func (ctx *TransactionContext) Signature() []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxSignature))
}

func (ctx *TransactionContext) ForEachInputID(fun func(idx byte, oid *ledger.OutputID) bool) {
	ctx.tree.ForEach(func(i byte, data []byte) bool {
		oid, err := ledger.OutputIDFromBytes(data)
		util.AssertNoError(err)
		if !fun(i, &oid) {
			return false
		}
		return true
	}, Path(ledger.TransactionBranch, ledger.TxInputIDs))
}

func (ctx *TransactionContext) ForEachEndorsement(fun func(idx byte, txid *ledger.TransactionID) bool) {
	ctx.tree.ForEach(func(i byte, data []byte) bool {
		txid, err := ledger.TransactionIDFromBytes(data)
		util.AssertNoError(err)
		if !fun(i, &txid) {
			return false
		}
		return true
	}, Path(ledger.TransactionBranch, ledger.TxEndorsements))
}

func (ctx *TransactionContext) ForEachProducedOutputData(fun func(idx byte, oData []byte) bool) {
	ctx.tree.ForEach(func(i byte, outputData []byte) bool {
		return fun(i, outputData)
	}, ledger.PathToProducedOutputs)
}

func (ctx *TransactionContext) ForEachProducedOutput(fun func(idx byte, out *ledger.Output, oid *ledger.OutputID) bool) {
	ctx.ForEachProducedOutputData(func(idx byte, oData []byte) bool {
		out, _ := ledger.OutputFromBytesReadOnly(oData)
		oid := ctx.OutputID(idx)
		if !fun(idx, out, &oid) {
			return false
		}
		return true
	})
}

func (ctx *TransactionContext) ForEachConsumedOutput(fun func(idx byte, oid *ledger.OutputID, out *ledger.Output) bool) {
	ctx.ForEachInputID(func(idx byte, oid *ledger.OutputID) bool {
		out, _ := ctx.ConsumedOutput(idx)
		if !fun(idx, oid, out) {
			return false
		}
		return true
	})
}

func (ctx *TransactionContext) ConsumedOutputData(idx byte) []byte {
	return ctx.tree.BytesAtPath(Path(ledger.ConsumedBranch, ledger.ConsumedOutputsBranch, idx))
}

func (ctx *TransactionContext) ConsumedOutput(idx byte) (*ledger.Output, error) {
	return ledger.OutputFromBytesReadOnly(ctx.ConsumedOutputData(idx))
}

func (ctx *TransactionContext) UnlockData(idx byte) []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxUnlockParams, idx))
}

func (ctx *TransactionContext) ProducedOutputData(idx byte) []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxOutputs, idx))
}

func (ctx *TransactionContext) ProducedOutput(idx byte) (*ledger.OutputWithID, error) {
	data := ctx.ProducedOutputData(idx)
	o, _, _, err := ledger.OutputFromBytesMain(data)
	if err != nil {
		return nil, err
	}
	return &ledger.OutputWithID{
		ID:     ctx.OutputID(idx),
		Output: o,
	}, err
}

func (ctx *TransactionContext) NumProducedOutputs() int {
	return ctx.tree.NumElements([]byte{ledger.TransactionBranch, ledger.TxOutputs})
}

func (ctx *TransactionContext) NumInputs() int {
	return ctx.tree.NumElements([]byte{ledger.TransactionBranch, ledger.TxInputIDs})
}

func (ctx *TransactionContext) NumEndorsements() int {
	return ctx.tree.NumElements([]byte{ledger.TransactionBranch, ledger.TxEndorsements})
}

func (ctx *TransactionContext) InputID(idx byte) ledger.OutputID {
	data := ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxInputIDs, idx))
	ret, err := ledger.OutputIDFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func (ctx *TransactionContext) MustTimestampData() ([]byte, ledger.LogicalTime) {
	ret := ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxTimestamp))
	retTs, err := ledger.LogicalTimeFromBytes(ret)
	util.AssertNoError(err)
	return ret, retTs
}

func (ctx *TransactionContext) SequencerAndStemOutputIndices() (byte, byte) {
	ret := ctx.tree.BytesAtPath(ledger.PathToSequencerAndStemOutputIndices)
	util.Assertf(len(ret) == 2, "len(ret)==2")
	return ret[0], ret[1]
}

func (ctx *TransactionContext) TotalAmountStored() uint64 {
	ret := ctx.tree.BytesAtPath(ledger.PathToTotalProducedAmount)
	util.Assertf(len(ret) == 8, "len(ret)==8")
	return binary.BigEndian.Uint64(ret)
}

func (ctx *TransactionContext) OutputID(idx byte) ledger.OutputID {
	return ledger.NewOutputID(ctx.txid, idx)
}
