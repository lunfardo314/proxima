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
	"golang.org/x/crypto/blake2b"
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

func (ctx *TxContext) _lines(utxoToLines func(o *ledger.Output, prefix ...string) *lines.Lines, prefix ...string) *lines.Lines {
	txid := ctx.ID()
	ret := lines.New(prefix...)
	ret.Add("Transaction id: %s, size: %d", txid.String(), len(ctx.TransactionBytes()))
	tsBin, ts := ctx.MustTimestampData()
	ret.Add("Timestamp: %s %s", easyfl_util.Fmt(tsBin), ts)

	seqIdx, stemIdx := ctx.SequencerAndStemOutputIndices()
	ret.Add("Sequencer output index: %d, sequencer milestone: %v", seqIdx, seqIdx != 0xff)
	ret.Add("Stem output index: %d, stem output: %v", stemIdx, seqIdx != 0xff && stemIdx != 0xff)

	ret.Add("Total consumed amounts: [%s]", util.ThSlice(ctx.TotalConsumedAmounts()...))
	ret.Add("Total produced amounts: [%s]", util.ThSlice(ctx.TotalProducedAmounts()...))
	ret.Add("Total amount produced (given): %s (0x%s)",
		util.Th(ctx.TotalAmountStored()), hex.EncodeToString(ctx.TotalAmountStoredBin()))

	inpCom := ctx.InputCommitment()
	ret.Add("Input commitment: %s", easyfl_util.Fmt(inpCom))
	h := ctx.ConsumedOutputHash()
	eqCom := ""
	if !bytes.Equal(inpCom, h[:]) {
		eqCom = "   !!! NOT EQUAL WITH INPUT COMMITMENT !!!!"
	}
	ret.Add("Consumed output hash: %s%s", easyfl_util.Fmt(h[:]), eqCom)
	sign := ctx.SignatureBytes()
	ret.Add("Signature: %s", easyfl_util.Fmt(sign))
	if len(sign) == 96 {
		sender := blake2b.Sum256(sign[64:])
		ret.Add("     ED25519 sender address: %s", easyfl_util.Fmt(sender[:]))
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
				cid = cc.ID
			}
			chainIdStr = "                      chainID: " + cid.StringShort()
		}
		ret.Add("  #%d %s", idx, oid.String()).
			Add("       bytes (%d): %s", len(o.Bytes()), hex.EncodeToString(o.Bytes()))
		if msd := ledger.ParseMilestoneData(o); msd != nil {
			ret.Add("       seq: %s", msd.Name)
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

func ValidateTxBytes(txBytes []byte, loadInput func(i byte) (*ledger.Output, error)) error {
	tx, err := FromBytes(txBytes, MainTxValidationOptions...)
	if err != nil {
		return err
	}
	ctx, err := TxContextFromTransaction(tx, loadInput)
	if err != nil {
		return err
	}
	return ctx.Validate()
}
