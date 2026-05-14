package foundry

import (
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	apiclient "github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initFoundryBurnCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "burn <chainID> <amount>",
		Short: "burn <amount> native tokens of tag <chainID> back into the foundry",
		Long: `Burn <amount> native tokens denominated in tag <chainID>.

A foundry transit is built that:
  - consumes the existing foundry chain output (the wallet must control
    its lock at index 2)
  - consumes wallet sigLock UTXOs carrying tokenAmount(<chainID>, _)
    totaling at least <amount>
  - produces the transited foundry with foundry.supply reduced by
    <amount>
  - if the consumed-token sum exceeds <amount>, produces a single
    tokenAmount(<chainID>, remainder) sigLock output back to the wallet
  - tag-along output + PRXI remainder

The token() balance equation is enforced via the same
`+"`"+`token(<chainID>, foundryProducedIdx)`+"`"+` declaration that TransitFoundry
pushes for mint -- the foundry-transit form already covers both
directions (mint = supply grows, burn = supply shrinks).`,
		Args: cobra.ExactArgs(2),
		Run:  runFoundryBurnCmd,
	}
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runFoundryBurnCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.Assertf(err == nil, "failed to parse chainID %q: %v", args[0], err)

	amount, err := strconv.ParseUint(args[1], 10, 64)
	glb.AssertNoError(err)
	glb.Assertf(amount > 0, "burn amount must be > 0")

	wallet := glb.GetWalletData()
	glb.Infof("wallet account: %s", wallet.Account.String())

	client := glb.GetClient()

	// Fetch the foundry chain output and read current supply.
	oData, lrbid, err := client.GetChainOutputData(chainID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	foundryIn, err := ledger.OutputFromBytesWithLib(oData.Data, ledger.L(oData.ID.Slot()))
	glb.AssertNoError(err)
	fBytes, err := foundryIn.ConstraintAt(ledger.ConstraintIndexFoundry)
	glb.Assertf(err == nil, "output %s has no foundry constraint at index %d: %v",
		oData.ID.StringShort(), ledger.ConstraintIndexFoundry, err)
	fIn, err := ledger.FoundryFromBytes(fBytes)
	glb.AssertNoError(err)
	glb.Assertf(amount <= fIn.Supply,
		"burn amount %s exceeds foundry supply %s",
		util.Th(amount), util.Th(fIn.Supply))
	newSupply := fIn.Supply - amount
	glb.Infof("foundry supply: %s -> %s", util.Th(fIn.Supply), util.Th(newSupply))

	// Tag-along setup.
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	// Fetch wallet sigLock UTXOs and split into token / pure-PRXI buckets.
	res, err := client.GetOutputs(wallet.Account.ControllerID(), apiclient.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   apiclient.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
	})
	glb.AssertNoError(err)

	var (
		tokenInputs []*ledger.OutputWithID
		tokenSum    uint64
		prxiInputs  []*ledger.OutputWithID
	)
	for _, o := range res.Outputs {
		if ta, found := outputTokenAmountForTag(o.Output, chainID); found {
			tokenInputs = append(tokenInputs, o)
			tokenSum += ta.Amount
			continue
		}
		if outputCarriesTokenAmount(o.Output) {
			// holds tokenAmount of a different tag - skip
			continue
		}
		prxiInputs = append(prxiInputs, o)
	}
	glb.Assertf(tokenSum >= amount,
		"insufficient native-token balance: have %s of tag %s, need %s",
		util.Th(tokenSum), chainID.StringShort(), util.Th(amount))

	// Greedy-select tokenAmount inputs until we cover `amount`.
	var (
		selectedTokenIns []*ledger.OutputWithID
		consumedTokenSum uint64
	)
	for _, o := range tokenInputs {
		selectedTokenIns = append(selectedTokenIns, o)
		ta, _ := outputTokenAmountForTag(o.Output, chainID)
		consumedTokenSum += ta.Amount
		if consumedTokenSum >= amount {
			break
		}
	}
	tokenRemainder := consumedTokenSum - amount

	const remainderTokenPRXI uint64 = 100_000_000

	// Estimate extra PRXI we need from pure-PRXI inputs. The foundry
	// transit preserves the foundry's own PRXI. Consumed PRXI comes from
	// the token inputs (their storage deposit); produced PRXI is the
	// optional remainder + tag-along.
	var consumedPRXIFromTokenIns uint64
	for _, o := range selectedTokenIns {
		consumedPRXIFromTokenIns += o.Output.TokenBalance()
	}
	producedFixedPRXI := feeAmount
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
		"insufficient PRXI to fund burn: need %s extra, have %s in pure-PRXI sigLock UTXOs",
		util.Th(neededExtraPRXI), util.Th(prxiSum))

	// Build the tx.
	txb := txbuilder.New()
	in := &ledger.OutputDataWithChainID{
		OutputDataWithID: *oData,
		ChainID:          chainID,
	}
	// Foundry becomes input 0 (TransitFoundry handles chain unlock +
	// token(chainID, succIdx) declaration).
	_, err = txb.TransitFoundry(in, newSupply)
	glb.AssertNoError(err)
	txb.PutSignatureUnlock(0)

	// Append tokenAmount inputs + PRXI inputs.
	rest := append([]*ledger.OutputWithID{}, selectedTokenIns...)
	rest = append(rest, selectedPRXIIns...)
	_, inTs, err := txb.ConsumeOutputsNoUnlock(rest...)
	glb.AssertNoError(err)
	for i := range rest {
		err = txb.PutUnlockReference(byte(1+i), ledger.ConstraintIndexLock, 0)
		glb.AssertNoError(err)
	}

	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	foundryTs := oData.ID.Timestamp().AddTicks(int(ledger.L(oData.ID.Slot()).TransactionPace))
	ts = base.MaximumTime(ts, foundryTs)
	ts = base.MaximumTime(ts, inTs)

	// Optional tokenAmount remainder back to the wallet.
	if tokenRemainder > 0 {
		remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(remainderTokenPRXI).WithLock(wallet.Account).WithTokenAmount(chainID, tokenRemainder)
		})
		glb.AssertNoError(remainderOut.EnoughAmountForStorageDeposit())
		_, err = txb.ProduceOutput(remainderOut)
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

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(wallet.PrivateKey)

	txBytes, txid, failedTx, err := txb.BytesWithValidation()
	glb.Assertf(err == nil, "build failed: %v\n---------- failing tx --------\n%s", err, failedTx)

	glb.Infof("burn plan:")
	glb.Infof("   foundry chainID:    %s", chainID.String())
	glb.Infof("   burning:            %s tokens", util.Th(amount))
	glb.Infof("   tokenAmount inputs: %d (sum %s)", len(selectedTokenIns), util.Th(consumedTokenSum))
	if tokenRemainder > 0 {
		glb.Infof("   token remainder:    %s back to wallet", util.Th(tokenRemainder))
	}
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

// outputTokenAmountForTag returns the first tokenAmount(tag, _) constraint
// found on the output, or (nil, false) if none.
func outputTokenAmountForTag(o *ledger.Output, tag base.ChainID) (*ledger.TokenAmount, bool) {
	for _, raw := range o.ConstraintsRawBytes() {
		ta, err := ledger.TokenAmountFromBytes(raw)
		if err == nil && ta.Tag == tag {
			return ta, true
		}
	}
	return nil, false
}
