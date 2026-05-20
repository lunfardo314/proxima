package foundry

import (
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	apiclient "github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
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
` + "`" + `token(<chainID>, foundryProducedIdx)` + "`" + ` declaration that the foundry
transit pushes for mint -- the foundry-transit form already covers
both directions (mint = supply grows, burn = supply shrinks).`,
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

	// Fetch the parsed foundry chain output.
	foundryIn, lrbid, err := client.GetChainOutput(chainID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	fBytes, err := foundryIn.Output.ConstraintAt(ledger.ConstraintIndexFoundry)
	glb.Assertf(err == nil, "output %s has no foundry constraint at index %d: %v",
		foundryIn.ID.StringShort(), ledger.ConstraintIndexFoundry, err)
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

	// Estimate extra PRXI we need from pure-PRXI inputs.
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

	// Wasm-style build via txbuildercore + helpers.
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderID(wallet.Account)
	txb := txbuildercore.New(0)

	// --- Input 0: the foundry chain output.
	foundryInBytes := foundryIn.Output.Bytes()
	txb.ConsumeOutput(foundryInBytes, foundryIn.ID)
	consumedBytes := [][]byte{foundryInBytes}
	txb.PutSignatureUnlock(0)

	// --- Compose the transited foundry output.
	cc := &foundryIn.ChainConstraint
	transitionBin, err := lib.NewChainTransition(
		chainID,
		0, // predInputIndex
		cc.OriginSlot,
		cc.CumulativeChainInflation,
		cc.CumulativeBranchBonus,
		cc.TransitionCounter+1,
		cc.BranchCounter,
	)
	glb.AssertNoError(err)
	newFoundryBin, err := lib.NewFoundryBytecode(newSupply)
	glb.AssertNoError(err)

	fb, err := txbuildercore.OutputBuilderFromBytes(foundryInBytes)
	glb.AssertNoError(err)
	fb.PutConstraint(transitionBin, ledger.ConstraintIndexChain)
	fb.PutConstraint(newFoundryBin, ledger.ConstraintIndexFoundry)
	foundryProducedIdx := txb.ProduceOutput(fb.Output().Bytes())

	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, txbuildercore.ChainUnlockParams(foundryProducedIdx))

	tokenDecl, err := lib.TokenFoundry(chainID, foundryProducedIdx)
	glb.AssertNoError(err)
	txb.PushTxConstraint(tokenDecl)

	// --- Append tokenAmount inputs + PRXI inputs at indices 1..N.
	rest := append([]*ledger.OutputWithID{}, selectedTokenIns...)
	rest = append(rest, selectedPRXIIns...)
	for i, in := range rest {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		err := txb.PutUnlockReference(byte(1+i), ledger.ConstraintIndexLock, 0)
		glb.AssertNoError(err)
	}

	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	foundryTs := foundryIn.ID.Timestamp().AddTicks(int(ledger.L(foundryIn.ID.Slot()).TransactionPace))
	ts = base.MaximumTime(ts, foundryTs)
	for _, in := range rest {
		ts = base.MaximumTime(ts, in.Timestamp())
	}

	// --- Optional tokenAmount remainder back to the wallet.
	if tokenRemainder > 0 {
		remainderBase, err := txbuildercore.NewSigLockOutput(lib, remainderTokenPRXI, walletHolderID)
		glb.AssertNoError(err)
		rb, err := txbuildercore.OutputBuilderFromBytes(remainderBase.Bytes())
		glb.AssertNoError(err)
		err = lib.AppendTokenAmountToOutput(rb, chainID, tokenRemainder)
		glb.AssertNoError(err)
		txb.ProduceOutput(rb.Output().Bytes())
	}

	// --- Tag-along.
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(tagAlongOut.Bytes())

	// --- PRXI remainder back to wallet.
	totalConsumedPRXI := foundryIn.Output.TokenBalance() + consumedPRXIFromTokenIns + prxiSum
	totalProducedFixed := foundryIn.Output.TokenBalance() + feeAmount
	if tokenRemainder > 0 {
		totalProducedFixed += remainderTokenPRXI
	}
	if totalConsumedPRXI > totalProducedFixed {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, totalConsumedPRXI-totalProducedFixed, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(wallet.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)

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

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}
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
