package node_cmd

import (
	"os"
	"time"

	"github.com/lunfardo314/proxima/api"
	apiclient "github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
)

// runSendTaggedCmd handles `proxi node send <amount> --tag <chainID>`.
// Builds a pure-conservation native-token transfer tx:
//   - consumes wallet's tokenAmount(tag, _) UTXOs totaling >= amount
//   - consumes pure-PRXI sigLock UTXOs to cover the recipient output's
//     storage deposit + tag-along fee + optional token-remainder deposit
//   - produces a sigLock/chainLock output to the target carrying
//     tokenAmount(tag, amount)
//   - if consumed-tokens > amount, produces a tokenAmount(tag, delta)
//     remainder UTXO back to the wallet
//   - tag-along output to the configured sequencer
//   - PRXI remainder back to the wallet
//   - pushes token(tag, 0x) at TxConstraints (Phase D auditability +
//     conservation equation Σ consumed = Σ produced)
//
// The wallet signs at input 0; remaining inputs reference input 0's
// signature unlock (standard sigLock pattern).
func runSendTaggedCmd(amount uint64, tagHex string) {
	tag, err := base.ChainIDFromHexString(tagHex)
	glb.Assertf(err == nil, "failed to parse --tag chainID %q: %v", tagHex, err)
	glb.Assertf(amount > 0, "transfer amount must be > 0")

	wallet := glb.GetWalletData()
	glb.Infof("source: wallet account %s", wallet.Account.String())

	targetCtrl := glb.MustGetTarget()
	glb.Infof("target: %s", targetCtrl.String())
	glb.Infof("tag:    %s (%s tokens)", tag.String(), util.Th(amount))

	// Tag-along setup (mirrors plain send path).
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified (set tag_along.sequencer_id)")
	feeAmount := glb.GetTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee not configured (set tag_along.fee)")
	md, err := glb.GetClient().GetSequencerData(*tagAlongSeqID)
	glb.AssertNoError(err)
	if md.MinimumFee() > feeAmount {
		feeAmount = md.MinimumFee()
	}

	// Storage-deposit budgets for newly produced outputs. We size on the
	// safe side; the actual storage minimum for a sigLock + tokenAmount
	// output is well under 100M (see ledger/tests/native_token_test.go).
	const recipientPRXI uint64 = 100_000_000
	const remainderTokenPRXI uint64 = 100_000_000

	client := glb.GetClient()

	// Fetch wallet sigLock UTXOs (non-chained). Split into:
	//   - tokenInputs: those carrying tokenAmount(tag, _)
	//   - prxiInputs: those carrying no tokenAmount constraint at all
	//   - others (tokenAmount of a different tag): skipped
	res, err := client.GetOutputs(wallet.Account.ControllerID(), apiclient.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   apiclient.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
	})
	glb.AssertNoError(err)
	glb.PrintLRB(&res.LRBID)

	var (
		tokenInputs []*ledger.OutputWithID
		tokenSum    uint64
		prxiInputs  []*ledger.OutputWithID
	)
	for _, o := range res.Outputs {
		if ta, found := pickTokenAmount(o.Output, tag); found {
			tokenInputs = append(tokenInputs, o)
			tokenSum += ta.Amount
			continue
		}
		if outputCarriesAnyTokenAmount(o.Output) {
			// holds a tokenAmount for a different tag - skip (we won't
			// touch other native-token bookkeeping in this tx)
			continue
		}
		prxiInputs = append(prxiInputs, o)
	}
	glb.Assertf(tokenSum >= amount,
		"insufficient native-token balance for tag %s: have %s, need %s",
		tag.StringShort(), util.Th(tokenSum), util.Th(amount))

	// Pick tokenAmount inputs greedily until the sum covers `amount`.
	var (
		selectedTokenIns []*ledger.OutputWithID
		consumedTokenSum uint64
	)
	for _, o := range tokenInputs {
		selectedTokenIns = append(selectedTokenIns, o)
		ta, _ := pickTokenAmount(o.Output, tag)
		consumedTokenSum += ta.Amount
		if consumedTokenSum >= amount {
			break
		}
	}

	tokenRemainder := consumedTokenSum - amount

	// Estimate PRXI needs. Inputs already contribute their token-output
	// PRXI to the consumed sum -- they fund storage deposits for the
	// recipient/remainder outputs.
	var consumedPRXIFromTokenIns uint64
	for _, o := range selectedTokenIns {
		consumedPRXIFromTokenIns += o.Output.TokenBalance()
	}
	producedFixedPRXI := recipientPRXI + feeAmount
	if tokenRemainder > 0 {
		producedFixedPRXI += remainderTokenPRXI
	}
	// PRXI we still need from pure-PRXI funding inputs (negative is fine
	// = token inputs already cover everything; we just need 0 funding).
	var neededExtraPRXI uint64
	if producedFixedPRXI > consumedPRXIFromTokenIns {
		neededExtraPRXI = producedFixedPRXI - consumedPRXIFromTokenIns
	}

	var (
		selectedPRXIIns []*ledger.OutputWithID
		prxiSum         uint64
	)
	for _, o := range prxiInputs {
		if prxiSum >= neededExtraPRXI {
			break
		}
		selectedPRXIIns = append(selectedPRXIIns, o)
		prxiSum += o.Output.TokenBalance()
	}
	glb.Assertf(prxiSum >= neededExtraPRXI,
		"insufficient PRXI to fund tagged transfer: need %s, have %s in pure-PRXI sigLock UTXOs",
		util.Th(neededExtraPRXI), util.Th(prxiSum))

	// Build the tx. tokenAmount inputs come first so input 0 is a
	// sigLock UTXO the wallet controls; signature unlock binds at idx 0.
	txb := txbuilder.New()
	allInputs := append([]*ledger.OutputWithID{}, selectedTokenIns...)
	allInputs = append(allInputs, selectedPRXIIns...)

	_, inTs, err := txb.ConsumeOutputsNoUnlock(allInputs...)
	glb.AssertNoError(err)
	for i := range allInputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}

	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	ts = base.MaximumTime(ts, inTs)

	// Recipient output: sigLock/chainLock to target + tokenAmount(tag, amount).
	recipientOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(recipientPRXI).WithLock(targetCtrl).WithTokenAmount(tag, amount)
	})
	glb.AssertNoError(recipientOut.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(recipientOut)
	glb.AssertNoError(err)

	// Optional tokenAmount remainder back to the wallet.
	if tokenRemainder > 0 {
		remainderTokenOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(remainderTokenPRXI).WithLock(wallet.Account).WithTokenAmount(tag, tokenRemainder)
		})
		glb.AssertNoError(remainderTokenOut.EnoughAmountForStorageDeposit())
		_, err = txb.ProduceOutput(remainderTokenOut)
		glb.AssertNoError(err)
	}

	// Tag-along.
	outTagAlong := ledger.NewTagAlongOutput(feeAmount, *tagAlongSeqID, base.HolderID(wallet.Account))
	_, err = txb.ProduceOutput(outTagAlong)
	glb.AssertNoError(err)

	// PRXI remainder.
	totalConsumed := txb.ConsumedAmount()
	totalProduced, _ := txb.ProducedAmount()
	if totalConsumed > totalProduced {
		remainder := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(totalConsumed - totalProduced).WithLock(wallet.Account)
		})
		_, err = txb.ProduceOutput(remainder)
		glb.AssertNoError(err)
	}

	// Phase D auditability + balance equation.
	txb.DeclareTokenConservation(tag)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(wallet.PrivateKey)

	txBytes, txid, failedTx, err := txb.BytesWithValidation()
	glb.Assertf(err == nil, "build failed: %v\n---------- failing tx --------\n%s", err, failedTx)

	glb.Infof("tagged send plan:")
	glb.Infof("   amount sent:        %s tokens of tag %s", util.Th(amount), tag.StringShort())
	glb.Infof("   tokenAmount inputs: %d (sum %s)", len(selectedTokenIns), util.Th(consumedTokenSum))
	if tokenRemainder > 0 {
		glb.Infof("   token remainder:    %s back to wallet", util.Th(tokenRemainder))
	}
	glb.Infof("   PRXI funding ins:   %d (sum %s)", len(selectedPRXIIns), util.Th(prxiSum))
	glb.Infof("   recipient output:   %s PRXI on-chain to %s", util.Th(recipientPRXI), targetCtrl.String())
	glb.Infof("   tag-along fee:      %s to %s", util.Th(feeAmount), tagAlongSeqID.StringShort())

	if !glb.YesNoPrompt("proceed?", true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	err = client.SubmitTransaction(txBytes)
	glb.AssertNoError(err)
	glb.Infof("transaction submitted: %s", txid.String())

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}

// pickTokenAmount returns the first tokenAmount(tag, _) constraint found
// on the output, or (nil, false) if none.
func pickTokenAmount(o *ledger.Output, tag base.ChainID) (*ledger.TokenAmount, bool) {
	for _, raw := range o.ConstraintsRawBytes() {
		ta, err := ledger.TokenAmountFromBytes(raw)
		if err == nil && ta.Tag == tag {
			return ta, true
		}
	}
	return nil, false
}

// outputCarriesAnyTokenAmount reports whether the output has any
// tokenAmount(...) constraint regardless of tag.
func outputCarriesAnyTokenAmount(o *ledger.Output) bool {
	for _, raw := range o.ConstraintsRawBytes() {
		if _, err := ledger.TokenAmountFromBytes(raw); err == nil {
			return true
		}
	}
	return false
}
