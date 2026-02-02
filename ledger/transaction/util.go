package transaction

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"slices"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

func SaveTransactionAsFile(txBytes []byte, fname ...string) error {
	var fn string
	if len(fname) > 0 {
		fn = fname[0]
	} else {
		txID, _, err := IDAndTimestampFromParsedTransactionBytes(txBytes)
		if err != nil {
			return err
		}
		fn = txID.AsFileName()
	}
	return os.WriteFile(fn, txBytes, 0644)
}

func (ctx *TxContext) String() string {
	return ctx.Lines().String()
}

func (ctx *TxContext) Lines(prefix ...string) *lines.Lines {
	return ctx._lines(func(o *ledger.Output, prefix ...string) *lines.Lines {
		return o.Lines(prefix...)
	}, prefix...)
}

func (ctx *TxContext) LinesSource(prefix ...string) *lines.Lines {
	return ctx._lines(func(o *ledger.Output, prefix ...string) *lines.Lines {
		return o.LinesSource(prefix...)
	}, prefix...)
}

func (ctx *TxContext) LinesHR(prefix ...string) *lines.Lines {
	return ctx._lines(func(o *ledger.Output, prefix ...string) *lines.Lines {
		return o.LinesHR(prefix...)
	}, prefix...)
}

func (ctx *TxContext) _lines(utxoToLines func(o *ledger.Output, prefix ...string) *lines.Lines, prefix ...string) *lines.Lines {
	txid := ctx.ID()
	ret := lines.New(prefix...)
	ret.Add("Transaction ID: %s, size: %d", txid.String(), len(ctx.TransactionBytes()))
	tsBin, ts := ctx.MustTimestampData()
	ret.Add("Timestamp: %s %s", easyfl_util.Fmt(tsBin), ts)

	if seqData := ctx.SequencerTransactionData(); seqData != nil {
		ret.Add("SEQUENCER TRANSACTION DATA:")
		ret.Append(seqData.Lines("    "))
	} else {
		ret.Add("NOT A SEQUENCER TRANSACTION")
	}

	ret.Add("Total consumed token balance: %s", util.Th(ctx.totalConsumedTokenBalance))
	ret.Add("Total produced amounts: [%s]", util.ThSlice(ctx.TotalProducedAmounts()...))

	inpCom := ctx.InputCommitment()
	ret.Add("Input commitment: %s", easyfl_util.Fmt(inpCom))
	h := ctx.ConsumedOutputHash()
	eqCom := ""
	if !bytes.Equal(inpCom, h[:]) {
		eqCom = "   !!! NOT EQUAL WITH INPUT COMMITMENT !!!!"
	}
	ret.Add("Consumed output hash: %s%s", easyfl_util.Fmt(h[:]), eqCom)
	sign, err := ctx.Signature()
	if err == nil {
		ret.Add("Signature: %s", sign.String())
	} else {
		ret.Add("Signature: err='%v'", err)
	}

	if explicitBaseline, ok := ctx.ExplicitBaseline(); ok {
		ret.Add("Explicit baseline: %s", explicitBaseline.String())
	}

	ret.Add("Endorsements (%d):", ctx.NumEndorsements())
	ctx.ForEachEndorsement(func(idx byte, txid *base.TransactionID) bool {
		ret.Add("  %d: %s", idx, txid.String())
		return true
	})

	ret.Add("Inputs (%d consumed outputs): ", ctx.NumInputs())
	ctx.ForEachConsumedOutput(func(idx byte, oid *base.OutputID, o *ledger.Output) bool {
		if o == nil {
			ret.Add("  #%d: %s (parse error)", idx, oid.String())
			return true
		}
		unlockBin := ctx.MustUnlockDataAt(idx)
		ret.Add("  #%d: %s", idx, oid.String()).
			Add("       bytes (%d): %s", len(o.Bytes()), hex.EncodeToString(o.Bytes())).
			Append(utxoToLines(o, "     ")).
			Add("     Unlock data: %s", UnlockDataToString(unlockBin))
		return true
	})

	ret.Add("Outputs (%d produced): ", ctx.NumProducedOutputs())
	totalSum := uint64(0)
	ctx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid *base.OutputID) bool {
		if o == nil {
			ret.Add("  #%d : parse error", idx)
			return true
		}
		totalSum += o.TokenBalance()
		chainIdStr := ""
		if cc, i := o.ChainConstraint(); i != 0xff {
			var cid base.ChainID
			if cc.IsOrigin() {
				oid1 := base.MustNewOutputID(txid, idx)
				cid = base.MakeOriginChainID(oid1)
			} else {
				cid = cc.ChainID
			}
			chainIdStr = "                      chainID: " + cid.StringShort()
		}
		ret.Add("  #%d %s", idx, oid.String()).
			Add("       bytes (%d): %s", len(o.Bytes()), hex.EncodeToString(o.Bytes()))
		if msd, err := ledger.ParseSequencerData(o); err == nil {
			ret.Add("       seq: %s", msd.Name())
		}
		ret.Append(utxoToLines(o, "     ")).
			Add(chainIdStr)
		return true
	})
	ret.Add("TOTAL: %s", util.Th(totalSum))
	return ret
}

func UnlockDataToString(data []byte) string {
	arr, err := tuples.TupleFromBytes(data)
	if err != nil {
		return fmt.Sprintf("error while parsing lazy array: %v", err)
	}
	return arr.String()
}

func ParseBytesToString(txBytes []byte, fetchOutput func(oid base.OutputID) ([]byte, bool)) string {
	ctx, err := TxContextFromTransferableBytes(txBytes, fetchOutput)
	if err != nil {
		return err.Error()
	}
	return ctx.String()
}

func PickOutputFromListFunc(lst []*ledger.OutputWithID) func(oid base.OutputID) ([]byte, bool) {
	return func(oid base.OutputID) ([]byte, bool) {
		idx := slices.IndexFunc(lst, func(o *ledger.OutputWithID) bool {
			return o.ID == oid
		})
		if idx < 0 {
			return nil, false
		}
		return lst[idx].Output.Bytes(), true
	}
}
