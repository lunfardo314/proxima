package node_cmd

import (
	"os"
	"time"

	"github.com/lunfardo314/proxima/api"
	apiclient "github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
)

// send_tagged.go: native-token send. Singleton-free build path —
// uses txbuildercore + the wallet library; no ledger.L() lookups.

// runSendTaggedCmd handles `proxi node send <amount> --tag <chainID>`.
// Builds a pure-conservation native-token transfer tx via the
// wasm-style wallet pipeline (txbuildercore + helpers):
//
//   - consumes wallet's tokenAmount(tag, _) UTXOs totaling >= amount
//   - consumes pure-PRXI sigLock UTXOs to cover the recipient output's
//     storage deposit + tag-along fee + optional token-remainder deposit
//   - produces a sigLock/chainLock output to the target carrying
//     tokenAmount(tag, amount)
//   - if consumed-tokens > amount, produces a tokenAmount(tag, delta)
//     remainder UTXO back to the wallet
//   - tag-along output to the configured sequencer
//   - PRXI remainder back to the wallet
//   - pushes token(tag, 0xFF) at TxConstraints (Phase D auditability +
//     conservation equation Σ consumed = Σ produced via TokenSentinel)
//
// The wallet signs at input 0; remaining inputs reference input 0's
// signature unlock (standard sigLock pattern).
func runSendTaggedCmd(amount uint64, tagHex string) {
	tag, err := base.ChainIDFromHexString(tagHex)
	glb.Assertf(err == nil, "failed to parse --tag chainID %q: %v", tagHex, err)
	glb.Assertf(amount > 0, "transfer amount must be > 0")

	wallet := glb.GetWalletData()
	walletHolderID := base.HolderID(wallet.Account)
	glb.Infof("source: wallet account %s", wallet.Account.String())

	targetCtrl := glb.MustGetTarget()
	glb.Infof("target: %s", targetCtrl.String())
	glb.Infof("tag:    %s (%s tokens)", tag.String(), util.Th(amount))

	// Tag-along setup (mirrors plain send path).
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified (set tag_along.sequencer_id)")
	feeAmount := glb.GetTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee not configured (set tag_along.fee)")
	seqMinFee, err := glb.GetSequencerMinimumFee(*tagAlongSeqID)
	glb.AssertNoError(err)
	if seqMinFee > feeAmount {
		feeAmount = seqMinFee
	}

	// Storage-deposit budgets for newly produced outputs. We size on the
	// safe side; the actual storage minimum for a sigLock + tokenAmount
	// output is well under 100M (see ledger/tests/native_token_test.go).
	const recipientPRXI uint64 = 100_000_000
	const remainderTokenPRXI uint64 = 100_000_000

	client := glb.GetClient()
	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()

	// Fetch wallet sigLock UTXOs (non-chained). Split into:
	//   - tokenInputs: those carrying tokenAmount(tag, _)
	//   - prxiInputs: those carrying no tokenAmount constraint at all
	//   - others (tokenAmount of a different tag): skipped
	res, err := client.GetOutputsForControllerID(wallet.Account.ControllerID(), apiclient.GetOutputsParams{
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
		if ta, found := pickTokenAmount(lib, o.Output, tag); found {
			tokenInputs = append(tokenInputs, o)
			tokenSum += ta.Amount
			continue
		}
		if outputCarriesAnyTokenAmount(lib, o.Output) {
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
		ta, _ := pickTokenAmount(lib, o.Output, tag)
		consumedTokenSum += ta.Amount
		if consumedTokenSum >= amount {
			break
		}
	}

	tokenRemainder := consumedTokenSum - amount

	var consumedPRXIFromTokenIns uint64
	for _, o := range selectedTokenIns {
		consumedPRXIFromTokenIns += o.Output.TokenBalance()
	}
	producedFixedPRXI := recipientPRXI + feeAmount
	if tokenRemainder > 0 {
		producedFixedPRXI += remainderTokenPRXI
	}
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

	allInputs := append([]*ledger.OutputWithID{}, selectedTokenIns...)
	allInputs = append(allInputs, selectedPRXIIns...)

	// Track input timestamps to derive the tx timestamp (pace constraint
	// requires ts > max(input timestamps) + transaction pace).
	inTs := base.NilLedgerTime
	for _, in := range allInputs {
		inTs = base.MaximumTime(inTs, in.Timestamp())
	}

	// =============================================================
	// Wasm-style build via txbuildercore + helpers.
	// =============================================================
	txb := txbuildercore.New(0)

	consumedBytes := make([][]byte, 0, len(allInputs))
	totalConsumed := uint64(0)
	for i, in := range allInputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		totalConsumed += in.Output.TokenBalance()
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}

	// Recipient output: sigLock/chainLock to target + tokenAmount(tag, amount).
	recipientOut, err := buildTokenLockedOutput(lib, recipientPRXI, targetCtrl, tag, amount)
	glb.AssertNoError(err)
	txb.ProduceOutput(recipientOut.Bytes())

	// Optional tokenAmount remainder back to the wallet (always sigLock).
	if tokenRemainder > 0 {
		remainderTokenOut, err := buildTokenLockedOutput(lib, remainderTokenPRXI, wallet.Account, tag, tokenRemainder)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderTokenOut.Bytes())
	}

	// Tag-along.
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(tagAlongOut.Bytes())

	// PRXI remainder back to the wallet (sigLock).
	totalProducedFixed := recipientPRXI + feeAmount
	if tokenRemainder > 0 {
		totalProducedFixed += remainderTokenPRXI
	}
	if totalConsumed > totalProducedFixed {
		prxiRemainderOut, err := txbuildercore.NewSigLockOutput(lib, totalConsumed-totalProducedFixed, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(prxiRemainderOut.Bytes())
	}

	// Phase D auditability + Σ conservation: push token(tag, 0xFF)
	// as a tx-level constraint.
	tokenSentinelBin, err := lib.TokenSentinel(tag)
	glb.AssertNoError(err)
	txb.PushTxConstraint(tokenSentinelBin)

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

	// Stamp + sign AFTER the prompt so the timestamp reflects the moment of
	// submission rather than the moment we offered the prompt; otherwise a
	// slow confirmation makes the tx "born stale".
	ts := consts.LedgerTimeFromClockTime(time.Now())
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	ts = base.MaximumTime(ts, inTs)
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(wallet.PrivateKey)

	txBytes := txb.Bytes()

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)
	glb.Infof("transaction submitted: %s", txid.String())

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}

// buildTokenLockedOutput composes an output of `prxi` PRXI locked to
// the given controller (sigLock or chainLock) carrying a
// tokenAmount(tag, amount) constraint. The base output bytes come
// from NewSigLockOutput / NewChainLockOutput; AppendTokenAmountToOutput
// adds the constraint + the dedup'd controller||tag compound entry to
// slot 1 (mirroring ledger.OutputBuilder.WithTokenAmount byte-for-byte).
func buildTokenLockedOutput(lib *txbuildercore.Library[any], prxi uint64, targetCtrl ledger.Controller, tag base.ChainID, amount uint64) (*txbuildercore.Output, error) {
	var baseOut *txbuildercore.Output
	var err error
	switch c := targetCtrl.(type) {
	case ledger.SigLock:
		baseOut, err = txbuildercore.NewSigLockOutput(lib, prxi, base.HolderID(c))
	case ledger.ChainLock:
		glb.Assertf(len(c) == 32, "chainLock target must carry a 32-byte chain ID, got %d", len(c))
		var chainID base.ChainID
		copy(chainID[:], c)
		baseOut, err = txbuildercore.NewChainLockOutput(lib, prxi, chainID)
	default:
		glb.Assertf(false, "send --tag only supports sigLock or chainLock targets, got %s", targetCtrl.Name())
	}
	if err != nil {
		return nil, err
	}
	b, err := txbuildercore.OutputBuilderFromBytes(baseOut.Bytes())
	if err != nil {
		return nil, err
	}
	if err := lib.AppendTokenAmountToOutput(b, tag, amount); err != nil {
		return nil, err
	}
	return b.Output(), nil
}

// pickTokenAmount returns the first tokenAmount(tag, _) constraint found
// on the output. Singleton-free — uses the wallet library.
func pickTokenAmount(lib *txbuildercore.Library[any], o *ledger.Output, tag base.ChainID) (txbuildercore.TokenAmountView, bool) {
	for _, raw := range o.ConstraintsRawBytes() {
		ta, err := lib.ParseTokenAmountBytecode(raw)
		if err == nil && ta.Tag == tag {
			return ta, true
		}
	}
	return txbuildercore.TokenAmountView{}, false
}

// outputCarriesAnyTokenAmount reports whether the output has any
// tokenAmount(...) constraint regardless of tag.
func outputCarriesAnyTokenAmount(lib *txbuildercore.Library[any], o *ledger.Output) bool {
	for _, raw := range o.ConstraintsRawBytes() {
		if _, err := lib.ParseTokenAmountBytecode(raw); err == nil {
			return true
		}
	}
	return false
}
