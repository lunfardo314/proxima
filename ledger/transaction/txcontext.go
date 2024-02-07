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

// TxContext is a data structure, which contains transferable transaction, consumed outputs and constraint library
type TxContext struct {
	tree        *lazybytes.Tree
	traceOption int
	// calculated and cached values
	txid            *ledger.TransactionID
	sender          ledger.AddressED25519
	inflationAmount uint64
	// EasyFL constraint validation context
	dataContext *ledger.DataContext
}

var Path = lazybytes.Path

const (
	TraceOptionNone = iota
	TraceOptionAll
	TraceOptionFailedConstraints
)

func TxContextFromTransaction(tx *Transaction, inputLoaderByIndex func(i byte) (*ledger.Output, error), traceOption ...int) (*TxContext, error) {
	ret := &TxContext{
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
			return nil, fmt.Errorf("TxContextFromTransaction: '%v'", err)
		}
		if o == nil {
			inpOid := tx.MustInputAt(byte(i))
			err = fmt.Errorf("TxContextFromTransaction: cannot get consumed output %s at input index %d of %s",
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

// TxContextFromTransferableBytes constructs lazybytes.Tree from transaction bytes and consumed outputs
func TxContextFromTransferableBytes(txBytes []byte, fetchInput func(oid *ledger.OutputID) ([]byte, bool), traceOption ...int) (*TxContext, error) {
	tx, err := FromBytes(txBytes, ScanSequencerData())
	if err != nil {
		return nil, err
	}
	return TxContextFromTransaction(tx, tx.InputLoaderByIndex(fetchInput), traceOption...)
}

// unlockScriptBinary finds script from unlock block
func (ctx *TxContext) unlockScriptBinary(invocationFullPath lazybytes.TreePath) []byte {
	unlockBlockPath := common.Concat(invocationFullPath)
	unlockBlockPath[1] = ledger.TxUnlockData
	return ctx.tree.BytesAtPath(unlockBlockPath)
}

func (ctx *TxContext) rootContext() easyfl.GlobalData {
	return ctx.evalContext(nil)
}

func (ctx *TxContext) TransactionBytes() []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch))
}

func (ctx *TxContext) TransactionID() *ledger.TransactionID {
	return ctx.txid
}

func (ctx *TxContext) InputCommitment() []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxInputCommitment))
}

func (ctx *TxContext) Signature() []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxSignature))
}

func (ctx *TxContext) ForEachInputID(fun func(idx byte, oid *ledger.OutputID) bool) {
	ctx.tree.ForEach(func(i byte, data []byte) bool {
		oid, err := ledger.OutputIDFromBytes(data)
		util.AssertNoError(err)
		if !fun(i, &oid) {
			return false
		}
		return true
	}, Path(ledger.TransactionBranch, ledger.TxInputIDs))
}

func (ctx *TxContext) ForEachEndorsement(fun func(idx byte, txid *ledger.TransactionID) bool) {
	ctx.tree.ForEach(func(i byte, data []byte) bool {
		txid, err := ledger.TransactionIDFromBytes(data)
		util.AssertNoError(err)
		if !fun(i, &txid) {
			return false
		}
		return true
	}, Path(ledger.TransactionBranch, ledger.TxEndorsements))
}

func (ctx *TxContext) ForEachProducedOutputData(fun func(idx byte, oData []byte) bool) {
	ctx.tree.ForEach(func(i byte, outputData []byte) bool {
		return fun(i, outputData)
	}, ledger.PathToProducedOutputs)
}

func (ctx *TxContext) ForEachProducedOutput(fun func(idx byte, out *ledger.Output, oid *ledger.OutputID) bool) {
	ctx.ForEachProducedOutputData(func(idx byte, oData []byte) bool {
		out, _ := ledger.OutputFromBytesReadOnly(oData)
		oid := ctx.OutputID(idx)
		if !fun(idx, out, &oid) {
			return false
		}
		return true
	})
}

func (ctx *TxContext) ForEachConsumedOutput(fun func(idx byte, oid *ledger.OutputID, out *ledger.Output) bool) {
	ctx.ForEachInputID(func(idx byte, oid *ledger.OutputID) bool {
		out, _ := ctx.ConsumedOutput(idx)
		if !fun(idx, oid, out) {
			return false
		}
		return true
	})
}

func (ctx *TxContext) ConsumedOutputData(idx byte) []byte {
	return ctx.tree.BytesAtPath(Path(ledger.ConsumedBranch, ledger.ConsumedOutputsBranch, idx))
}

func (ctx *TxContext) ConsumedOutput(idx byte) (*ledger.Output, error) {
	return ledger.OutputFromBytesReadOnly(ctx.ConsumedOutputData(idx))
}

func (ctx *TxContext) UnlockDataAt(idx byte) []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxUnlockData, idx))
}

func (ctx *TxContext) ProducedOutputData(idx byte) []byte {
	return ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxOutputs, idx))
}

func (ctx *TxContext) ProducedOutput(idx byte) (*ledger.OutputWithID, error) {
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

func (ctx *TxContext) NumProducedOutputs() int {
	return ctx.tree.NumElements([]byte{ledger.TransactionBranch, ledger.TxOutputs})
}

func (ctx *TxContext) NumInputs() int {
	return ctx.tree.NumElements([]byte{ledger.TransactionBranch, ledger.TxInputIDs})
}

func (ctx *TxContext) NumEndorsements() int {
	return ctx.tree.NumElements([]byte{ledger.TransactionBranch, ledger.TxEndorsements})
}

func (ctx *TxContext) InputID(idx byte) ledger.OutputID {
	data := ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxInputIDs, idx))
	ret, err := ledger.OutputIDFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func (ctx *TxContext) MustTimestampData() ([]byte, ledger.Time) {
	ret := ctx.tree.BytesAtPath(Path(ledger.TransactionBranch, ledger.TxTimestamp))
	retTs, err := ledger.TimeFromBytes(ret)
	util.AssertNoError(err)
	return ret, retTs
}

func (ctx *TxContext) SequencerAndStemOutputIndices() (byte, byte) {
	ret := ctx.tree.BytesAtPath(ledger.PathToSequencerAndStemOutputIndices)
	util.Assertf(len(ret) == 2, "len(ret)==2")
	return ret[0], ret[1]
}

func (ctx *TxContext) TotalAmountStored() uint64 {
	ret := ctx.tree.BytesAtPath(ledger.PathToTotalProducedAmount)
	util.Assertf(len(ret) == 8, "len(ret)==8")
	return binary.BigEndian.Uint64(ret)
}

func (ctx *TxContext) OutputID(idx byte) ledger.OutputID {
	return ledger.NewOutputID(ctx.txid, idx)
}
