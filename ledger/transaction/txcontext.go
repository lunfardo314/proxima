package transaction

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

// TxContext is a data structure, which contains transferable transaction, consumed outputs and constraint library
type TxContext struct {
	tree        *tuples.Tree
	traceOption int
	// calculated and cached values
	txid            base.TransactionID
	sender          ledger.AddressED25519
	inflationAmount uint64
	// EasyFL constraint validation context
	dataContext *base.DataContext
}

var Path = tuples.Path

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
	consumedOutputsArray := tuples.EmptyTupleEditable(256)
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
		consumedOutputsArray.MustPush(o.Bytes())
	}
	e := tuples.MakeTupleFromSerializableElements(consumedOutputsArray) // one level deeper
	ret.tree = tuples.TreeFromTreesReadOnly(tx.tree, e.AsTree())
	// always check the consistency of the transaction with the input context
	if err := ret.validateInputCommitmentSafe(); err != nil {
		return nil, fmt.Errorf("TxContextFromTransaction: %w\n>>>>>>>>>>>>>>>>>>\n%s", err, ret.String())
	}
	ret.dataContext = base.NewDataContext(ret.tree)
	return ret, nil
}

// TxContextFromTransferableBytes constructs tuples.Tree from transaction bytes and consumed outputs
func TxContextFromTransferableBytes(txBytes []byte, fetchInput func(oid base.OutputID) ([]byte, bool), traceOption ...int) (*TxContext, error) {
	tx, err := FromBytes(txBytes, ParseTotalProducedAmount, ParseSequencerData, ScanOutputs)
	if err != nil {
		return nil, err
	}
	return TxContextFromTransaction(tx, tx.InputLoaderByIndex(fetchInput), traceOption...)
}

// unlockScriptBinary finds the script from the data of unlock block
func (ctx *TxContext) unlockScriptBinary(invocationFullPath tuples.TreePath) []byte {
	unlockBlockPath := common.Concat(invocationFullPath)
	unlockBlockPath[1] = ledger.TxUnlockData
	return ctx.tree.MustBytesAtPath(unlockBlockPath)
}

func (ctx *TxContext) rootContext() easyfl.GlobalData {
	return ctx.makeEvalContext(nil)
}

func (ctx *TxContext) TransactionBytes() []byte {
	return ctx.tree.MustBytesAtPath(Path(ledger.TransactionBranch))
}

func (ctx *TxContext) TransactionID() base.TransactionID {
	return ctx.txid
}

func (ctx *TxContext) InputCommitment() []byte {
	return ctx.tree.MustBytesAtPath(Path(ledger.TransactionBranch, ledger.TxInputCommitment))
}

func (ctx *TxContext) ExplicitBaseline() (base.TransactionID, bool) {
	data := ctx.tree.MustBytesAtPath(Path(ledger.TransactionBranch, ledger.TxExplicitBaseline))
	if len(data) == 0 {
		return base.TransactionID{}, false
	}
	ret, err := base.TransactionIDFromBytes(data)
	util.AssertNoError(err)
	return ret, true
}

func (ctx *TxContext) Signature() []byte {
	return ctx.tree.MustBytesAtPath(Path(ledger.TransactionBranch, ledger.TxSignature))
}

func (ctx *TxContext) ForEachInputID(fun func(idx byte, oid *base.OutputID) bool) {
	err := ctx.tree.ForEach(func(i byte, data []byte) bool {
		oid, err := base.OutputIDFromBytes(data)
		util.AssertNoError(err)
		if !fun(i, &oid) {
			return false
		}
		return true
	}, Path(ledger.TransactionBranch, ledger.TxInputIDs))
	util.AssertNoError(err)
}

func (ctx *TxContext) ForEachEndorsement(fun func(idx byte, txid *base.TransactionID) bool) {
	err := ctx.tree.ForEach(func(i byte, data []byte) bool {
		txid, err := base.TransactionIDFromBytes(data)
		util.AssertNoError(err)
		if !fun(i, &txid) {
			return false
		}
		return true
	}, Path(ledger.TransactionBranch, ledger.TxEndorsements))
	util.AssertNoError(err)
}

func (ctx *TxContext) ForEachProducedOutputData(fun func(idx byte, oData []byte) bool) {
	ctx.tree.ForEach(func(i byte, outputData []byte) bool {
		return fun(i, outputData)
	}, ledger.PathToProducedOutputs)
}

func (ctx *TxContext) ForEachProducedOutput(fun func(idx byte, out *ledger.Output, oid *base.OutputID) bool) {
	ctx.ForEachProducedOutputData(func(idx byte, oData []byte) bool {
		out, _ := ledger.OutputFromBytesReadOnly(oData)
		oid := ctx.OutputID(idx)
		if !fun(idx, out, &oid) {
			return false
		}
		return true
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

func (ctx *TxContext) ConsumedOutputData(idx byte) []byte {
	return ctx.tree.MustBytesAtPath(Path(ledger.ConsumedBranch, ledger.ConsumedOutputsBranch, idx))
}

func (ctx *TxContext) ConsumedOutput(idx byte) (*ledger.Output, error) {
	return ledger.OutputFromBytesReadOnly(ctx.ConsumedOutputData(idx))
}

func (ctx *TxContext) UnlockDataAt(idx byte) []byte {
	return ctx.tree.MustBytesAtPath(Path(ledger.TransactionBranch, ledger.TxUnlockData, idx))
}

func (ctx *TxContext) ProducedOutputData(idx byte) []byte {
	return ctx.tree.MustBytesAtPath(Path(ledger.TransactionBranch, ledger.TxOutputs, idx))
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
	return ctx.tree.MustNumElementsAtPath([]byte{ledger.TransactionBranch, ledger.TxOutputs})
}

func (ctx *TxContext) NumInputs() int {
	return ctx.tree.MustNumElementsAtPath([]byte{ledger.TransactionBranch, ledger.TxInputIDs})
}

func (ctx *TxContext) NumEndorsements() int {
	return ctx.tree.MustNumElementsAtPath([]byte{ledger.TransactionBranch, ledger.TxEndorsements})
}

func (ctx *TxContext) InputID(idx byte) base.OutputID {
	data := ctx.tree.MustBytesAtPath(Path(ledger.TransactionBranch, ledger.TxInputIDs, idx))
	ret, err := base.OutputIDFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func (ctx *TxContext) MustTimestampData() ([]byte, base.LedgerTime) {
	ret := ctx.tree.MustBytesAtPath(Path(ledger.TransactionBranch, ledger.TxTimestamp))
	retTs, err := base.LedgerTimeFromBytes(ret)
	util.AssertNoError(err)
	return ret, retTs
}

func (ctx *TxContext) SequencerAndStemOutputIndices() (byte, byte) {
	ret := ctx.tree.MustBytesAtPath(ledger.PathToSequencerAndStemOutputIndices)
	util.Assertf(len(ret) == 2, "len(ret)==2")
	return ret[0], ret[1]
}

func (ctx *TxContext) TotalAmountStoredBin() []byte {
	return ctx.tree.MustBytesAtPath(ledger.PathToTotalProducedAmount)
}

func (ctx *TxContext) TotalAmountStored() uint64 {
	return easyfl_util.MustUint64FromBytes(ctx.TotalAmountStoredBin())
}

func (ctx *TxContext) TotalInflation() uint64 {
	return ctx.inflationAmount
}

func (ctx *TxContext) OutputID(idx byte) base.OutputID {
	return base.MustNewOutputID(ctx.txid, idx)
}

func (ctx *TxContext) Tree() *tuples.Tree {
	return ctx.tree
}
