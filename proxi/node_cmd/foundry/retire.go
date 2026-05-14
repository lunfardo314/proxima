package foundry

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
	"github.com/spf13/cobra"
)

func initFoundryRetireCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retire <chainID>",
		Short: "discontinue (retire) the foundry chain identified by <chainID>",
		Long: `Retire the foundry chain identified by <chainID>: consume the
foundry output and discontinue the chain (no produced successor). The
PRXI on-chain balance is moved to a target sigLock (default: wallet).

Policy enforcement is applied normally on the consumed side:
  - if the foundry carries no policy at index 5, retire succeeds under
    the chain-controller signature alone
  - if the foundry carries foundryNonDestructible, retire is rejected
    unless the foundry's supply is 0 (burn all minted tokens first via
    proxi node foundry burn)

This tx does NOT consume or produce any tokenAmount UTXOs and pushes
no token(...) declaration: the foundry's supply field (whatever its
value) is destroyed along with the foundry output, but no native
tokens move. Outstanding circulating tokens remain in their existing
UTXOs and become permanently un-burnable.`,
		Args: cobra.ExactArgs(1),
		Run:  runFoundryRetireCmd,
	}
	glb.AddFlagTarget(cmd)
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runFoundryRetireCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.Assertf(err == nil, "failed to parse chainID %q: %v", args[0], err)

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
	if fIn.Supply > 0 {
		glb.Infof("WARNING: foundry supply is %s -- retirement will be rejected by foundryNonDestructible if attached",
			util.Th(fIn.Supply))
	}
	foundryPRXI := foundryIn.TokenBalance()

	// Tag-along setup.
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	// Fetch wallet pure-PRXI sigLock UTXOs for tag-along fee + remainder
	// storage deposit. Skip tokenAmount-bearing UTXOs.
	res, err := client.GetOutputs(wallet.Account.ControllerID(), apiclient.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   apiclient.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: feeAmount,
	})
	glb.AssertNoError(err)

	var (
		fundingIns []*ledger.OutputWithID
		fundingSum uint64
	)
	for _, o := range res.Outputs {
		if outputCarriesTokenAmount(o.Output) {
			continue
		}
		fundingIns = append(fundingIns, o)
		fundingSum += o.Output.TokenBalance()
		if fundingSum >= feeAmount {
			break
		}
	}
	glb.Assertf(fundingSum >= feeAmount,
		"insufficient pure-PRXI wallet UTXOs to fund retire: need %s, have %s",
		util.Th(feeAmount), util.Th(fundingSum))

	// Build the tx. Foundry as input 0 (sigLock at index 2 covers it);
	// chain unlock parameters set to "discontinue".
	txb := txbuilder.New()
	foundryInIdx, err := txb.ConsumeOutput(foundryIn, oData.ID)
	glb.AssertNoError(err)
	txb.PutSignatureUnlock(foundryInIdx)
	txb.PutUnlockParams(foundryInIdx, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	// Append funding inputs (1..N).
	_, inTs, err := txb.ConsumeOutputsNoUnlock(fundingIns...)
	glb.AssertNoError(err)
	for i := range fundingIns {
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

	// Move the foundry's on-chain PRXI to the target.
	retiredOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(foundryPRXI).WithLock(target)
	})
	glb.AssertNoError(retiredOut.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(retiredOut)
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

	glb.Infof("retire plan:")
	glb.Infof("   foundry chainID:  %s", chainID.String())
	glb.Infof("   foundry supply:   %s (will be destroyed)", util.Th(fIn.Supply))
	glb.Infof("   PRXI moving:      %s to %s", util.Th(foundryPRXI), target.String())
	glb.Infof("   tag-along fee:    %s to %s", util.Th(feeAmount), tagAlongSeqID.StringShort())

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
