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

func initFoundryMintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mint <chainID> <amount>",
		Short: "mint <amount> native tokens of tag <chainID> to a target lock",
		Long: `Mint <amount> native tokens denominated in tag <chainID>.

A foundry transit is built that consumes the existing foundry chain
output and produces:
  - the transited foundry with foundry.supply increased by <amount>
  - a sigLock output to the target (default: wallet account) carrying
    tokenAmount(<chainID>, <amount>)
  - a tag-along output to the configured sequencer
  - any PRXI remainder back to the wallet

After the first mint, the foundry's tag becomes the real chain ID
(equal to <chainID>) and the tag-equals-chainID invariant is enforced
on every subsequent transit. Any policy script attached at index 5 is
evaluated as usual; foundryMaxSupply($0) will reject a mint that grows
foundry.supply above its cap.

The wallet must control the foundry (its lock at output index 2 must
be the wallet's sigLock).`,
		Args: cobra.ExactArgs(2),
		Run:  runFoundryMintCmd,
	}
	glb.AddFlagTarget(cmd)
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runFoundryMintCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.Assertf(err == nil, "failed to parse chainID %q: %v", args[0], err)

	amount, err := strconv.ParseUint(args[1], 10, 64)
	glb.AssertNoError(err)
	glb.Assertf(amount > 0, "mint amount must be > 0")

	wallet := glb.GetWalletData()
	glb.Infof("wallet account: %s", wallet.Account.String())

	target := glb.MustGetTarget()

	client := glb.GetClient()

	// Fetch the foundry chain output.
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
	glb.Infof("foundry current supply: %s", util.Th(fIn.Supply))
	newSupply := fIn.Supply + amount
	glb.Assertf(newSupply >= fIn.Supply, "supply overflow: %d + %d", fIn.Supply, amount)

	// Tag-along setup.
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	// The minted tokenAmount output's storage deposit + tag-along fee
	// come from wallet sig-lock funding. Storage minimum is well under
	// 100M for a simple sigLock + tokenAmount output; we pick the wallet
	// funding to cover that plus the tag-along fee.
	const mintedOutputAmount uint64 = 100_000_000
	needed := mintedOutputAmount + feeAmount
	res, err := client.GetOutputs(wallet.Account.ControllerID(), apiclient.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   apiclient.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: needed,
	})
	glb.AssertNoError(err)
	glb.PrintLRB(&res.LRBID)

	// Filter out wallet UTXOs that carry tokenAmount(...) constraints.
	// Pulling them in as PRXI funding would add their native-token amount
	// to the consumed side of the token() balance equation and force us
	// to re-produce or burn them — which is not what `mint` does. The
	// user can transfer/burn them in separate txs via `proxi node send
	// --tag` / `proxi node foundry burn`.
	var (
		walletOutputs   []*ledger.OutputWithID
		availableForPRXI uint64
	)
	for _, o := range res.Outputs {
		if outputCarriesTokenAmount(o.Output) {
			continue
		}
		walletOutputs = append(walletOutputs, o)
		availableForPRXI += o.Output.TokenBalance()
		if availableForPRXI >= needed {
			break
		}
	}
	glb.Assertf(availableForPRXI >= needed,
		"not enough pure-PRXI wallet UTXOs to fund mint. Need %s, have %s (excluding tokenAmount-bearing UTXOs)",
		util.Th(needed), util.Th(availableForPRXI))

	// Build the tx. Foundry transit goes first so the foundry is input 0
	// (TransitFoundry pushes both the consumed input and the produced
	// successor + chain unlock + token() declaration).
	txb := txbuilder.New()
	in := &ledger.OutputDataWithChainID{
		OutputDataWithID: *oData,
		ChainID:          chainID,
	}
	foundryProducedIdx, err := txb.TransitFoundry(in, newSupply)
	glb.AssertNoError(err)
	_ = foundryProducedIdx

	// Signature unlock for the foundry's sigLock at input 0.
	txb.PutSignatureUnlock(0)

	// Append wallet sig-lock funding inputs starting at index 1.
	_, inTs, err := txb.ConsumeOutputsNoUnlock(walletOutputs...)
	glb.AssertNoError(err)
	for i := range walletOutputs {
		err = txb.PutUnlockReference(byte(1+i), ledger.ConstraintIndexLock, 0)
		glb.AssertNoError(err)
	}

	// Timestamp = max(foundry input ts + pace, funding inputs ts, ledger now).
	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	foundryTs := oData.ID.Timestamp().AddTicks(int(ledger.L(oData.ID.Slot()).TransactionPace))
	ts = base.MaximumTime(ts, foundryTs)
	ts = base.MaximumTime(ts, inTs)

	// Mint output: sigLock-locked tokenAmount-bearing UTXO to the target.
	mintedOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(mintedOutputAmount).WithLock(target).WithTokenAmount(chainID, amount)
	})
	glb.AssertNoError(mintedOut.EnoughAmountForStorageDeposit())
	mintedIdx, err := txb.ProduceOutput(mintedOut)
	glb.AssertNoError(err)

	// Tag-along output.
	outTagAlong := ledger.NewTagAlongOutput(feeAmount, *tagAlongSeqID, base.HolderID(wallet.Account))
	_, err = txb.ProduceOutput(outTagAlong)
	glb.AssertNoError(err)

	// PRXI remainder back to the wallet.
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

	glb.Infof("mint plan:")
	glb.Infof("   foundry chainID:    %s", chainID.String())
	glb.Infof("   supply: %s -> %s", util.Th(fIn.Supply), util.Th(newSupply))
	glb.Infof("   minting:            %s tokens", util.Th(amount))
	glb.Infof("   minted output idx:  %d (%s PRXI on-chain to %s)",
		mintedIdx, util.Th(mintedOutputAmount), target.String())
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

// outputCarriesTokenAmount reports whether the output has any
// tokenAmount(...) constraint among its bytecode positions.
func outputCarriesTokenAmount(o *ledger.Output) bool {
	for _, raw := range o.ConstraintsRawBytes() {
		if _, err := ledger.TokenAmountFromBytes(raw); err == nil {
			return true
		}
	}
	return false
}
