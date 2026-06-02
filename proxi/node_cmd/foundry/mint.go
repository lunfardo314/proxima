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
  - any base-token remainder back to the wallet

After the first mint, the chain ID becomes real (equal to <chainID>);
the foundry's tag is always that chain ID (the foundry constraint has
no tag arg of its own). Any policy script attached at index 5 is
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
	chainID, err := base.ChainIDFromHexString(args[0])
	glb.Assertf(err == nil, "failed to parse chainID %q: %v", args[0], err)

	amount, err := strconv.ParseUint(args[1], 10, 64)
	glb.AssertNoError(err)
	glb.Assertf(amount > 0, "mint amount must be > 0")

	wallet := glb.GetWalletData()
	glb.Infof("wallet account: %s", wallet.Account.String())

	target := glb.MustGetTarget()

	client := glb.GetClient()

	// Fetch the parsed foundry chain output (carries chain metadata).
	foundryIn, lrbid, err := client.GetChainOutput(chainID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()

	fBytes, err := foundryIn.Output.ConstraintAt(ledger.ConstraintIndexFoundry)
	glb.Assertf(err == nil, "output %s has no foundry constraint at index %d: %v",
		foundryIn.ID.StringShort(), ledger.ConstraintIndexFoundry, err)
	fIn, err := lib.ParseFoundryBytecode(fBytes)
	glb.AssertNoError(err)
	glb.Infof("foundry current supply: %s", util.Th(fIn.Supply))
	newSupply := fIn.Supply + amount
	glb.Assertf(newSupply >= fIn.Supply, "supply overflow: %d + %d", fIn.Supply, amount)

	// Tag-along setup.
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	// Storage minimum for a simple sigLock + tokenAmount output is well
	// under 100M; we pick the wallet funding to cover that + the fee.
	const mintedOutputAmount uint64 = 100_000_000
	needed := mintedOutputAmount + feeAmount
	res, err := client.GetOutputsForControllerID(wallet.Account.ControllerID(), apiclient.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   apiclient.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: needed,
	})
	glb.AssertNoError(err)
	glb.PrintLRB(&res.LRBID)

	// Filter out wallet UTXOs carrying tokenAmount(...) — pulling them in
	// would unbalance the token() equation. The user can transfer/burn
	// them in separate txs via `proxi node send --tag` / `foundry burn`.
	var (
		walletOutputs       []*ledger.OutputWithID
		availableBaseTokens uint64
	)
	for _, o := range res.Outputs {
		if outputCarriesTokenAmount(o.Output) {
			continue
		}
		walletOutputs = append(walletOutputs, o)
		availableBaseTokens += o.Output.TokenBalance()
		if availableBaseTokens >= needed {
			break
		}
	}
	glb.Assertf(availableBaseTokens >= needed,
		"not enough pure base-token wallet UTXOs to fund mint. Need %s, have %s (excluding tokenAmount-bearing UTXOs)",
		util.Th(needed), util.Th(availableBaseTokens))

	// Wasm-style build via txbuildercore + helpers.
	walletHolderID := base.HolderIDFromED25519PrivateKey(wallet.PrivateKey)
	txb := txbuildercore.New(0)

	// --- Input 0: the foundry chain output.
	foundryInBytes := foundryIn.Output.Bytes()
	txb.ConsumeOutput(foundryInBytes, foundryIn.ID)
	consumedBytes := [][]byte{foundryInBytes}

	// Signature unlock for foundry's sigLock at input 0.
	txb.PutSignatureUnlock(0)

	// --- Compose the transited foundry output (chain transition + new
	// foundry supply + carried-over policy at index 5).
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
	// Policy at index 5 carries over from foundryInBytes.
	foundryProducedIdx := txb.ProduceOutput(fb.Output().Bytes())

	// Chain unlock params point at the produced foundry output index.
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, txbuildercore.ChainUnlockParams(foundryProducedIdx))

	// Push the tx-level token() declaration: token(chainID, foundryProducedIdx).
	tokenDecl, err := lib.TokenFoundry(chainID, foundryProducedIdx)
	glb.AssertNoError(err)
	txb.PushTxConstraint(tokenDecl)

	// --- Wallet sig-lock funding inputs starting at index 1.
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		err := txb.PutUnlockReference(byte(1+i), ledger.ConstraintIndexLock, 0)
		glb.AssertNoError(err)
	}

	// --- Mint output: sigLock-locked tokenAmount-bearing UTXO to the target.
	mintBase, err := glb.BuildLockOutput(lib, mintedOutputAmount, target)
	glb.AssertNoError(err)
	mb, err := txbuildercore.OutputBuilderFromBytes(mintBase.Bytes())
	glb.AssertNoError(err)
	err = lib.AppendTokenAmountToOutput(mb, chainID, amount)
	glb.AssertNoError(err)
	mintedIdx := txb.ProduceOutput(mb.Output().Bytes())

	// --- Tag-along output.
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(tagAlongOut.Bytes())

	// --- base-token remainder back to the wallet.
	totalConsumed := foundryIn.Output.TokenBalance() + availableBaseTokens
	totalProducedFixed := foundryIn.Output.TokenBalance() + mintedOutputAmount + feeAmount
	if totalConsumed > totalProducedFixed {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, totalConsumed-totalProducedFixed, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}

	glb.Infof("mint plan:")
	glb.Infof("   foundry chainID:    %s", chainID.String())
	glb.Infof("   supply: %s -> %s", util.Th(fIn.Supply), util.Th(newSupply))
	glb.Infof("   minting:            %s tokens", util.Th(amount))
	glb.Infof("   minted output idx:  %d (%s base tokens on-chain to %s)",
		mintedIdx, util.Th(mintedOutputAmount), target.String())
	glb.Infof("   tag-along fee:      %s to %s", util.Th(feeAmount), tagAlongSeqID.StringShort())

	if !glb.YesNoPrompt("proceed?", true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Stamp + sign AFTER the prompt so the timestamp reflects the moment of
	// submission, not the moment the user was offered the prompt. Otherwise
	// a slow confirmation makes the tx "born stale" and the boot sequencer
	// purges its tag-along output from the backlog before it can be drained.
	// Timestamp = max(now, foundry input ts + pace, funding inputs ts).
	ts := glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	foundryTs := foundryIn.ID.Timestamp().AddTicks(int(consts.TransactionPace))
	ts = base.MaximumTime(ts, foundryTs)
	for _, in := range walletOutputs {
		ts = base.MaximumTime(ts, in.Timestamp())
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(wallet.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}
	glb.Infof("transaction submitted: %s", txid.String())

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}

// outputCarriesTokenAmount reports whether the output has any
// tokenAmount(...) constraint among its bytecode positions. Uses the
// wallet-library parser — no ledger.L() singleton.
func outputCarriesTokenAmount(o *ledger.Output) bool {
	lib := glb.GetTxLibrary()
	for _, raw := range o.ConstraintsRawBytes() {
		if _, err := lib.ParseTokenAmountBytecode(raw); err == nil {
			return true
		}
	}
	return false
}
