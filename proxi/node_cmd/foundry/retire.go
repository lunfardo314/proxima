package foundry

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
	chainID, err := base.ChainIDFromHexString(args[0])
	glb.Assertf(err == nil, "failed to parse chainID %q: %v", args[0], err)

	wallet := glb.GetWalletData()
	glb.Infof("wallet account: %s", wallet.Account.String())

	target := glb.MustGetTarget()

	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	client := glb.GetClient()

	// Fetch the parsed foundry chain output.
	foundryIn, lrbid, err := client.GetChainOutput(chainID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	fBytes, err := foundryIn.Output.ConstraintAt(ledger.ConstraintIndexFoundry)
	glb.Assertf(err == nil, "output %s has no foundry constraint at index %d: %v",
		foundryIn.ID.StringShort(), ledger.ConstraintIndexFoundry, err)
	fIn, err := lib.ParseFoundryBytecode(fBytes)
	glb.AssertNoError(err)
	if fIn.Supply > 0 {
		glb.Infof("WARNING: foundry supply is %s -- retirement will be rejected by foundryNonDestructible if attached",
			util.Th(fIn.Supply))
	}
	foundryPRXI := foundryIn.Output.TokenBalance()

	// Tag-along setup.
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	// Fetch wallet pure-PRXI sigLock UTXOs for tag-along fee + remainder
	// storage deposit. Skip tokenAmount-bearing UTXOs.
	res, err := client.GetOutputsForControllerID(wallet.Account.ControllerID(), apiclient.GetOutputsParams{
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

	// Wasm-style build via txbuildercore + helpers.
	walletHolderID := base.HolderIDFromED25519PrivateKey(wallet.PrivateKey)
	txb := txbuildercore.New(0)

	// --- Input 0: the foundry output. Chain unlock = "discontinue"
	// (empty unlock-params).
	foundryInBytes := foundryIn.Output.Bytes()
	txb.ConsumeOutput(foundryInBytes, foundryIn.ID)
	consumedBytes := [][]byte{foundryInBytes}
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, txbuildercore.FinishChainUnlockParams)

	// --- Funding inputs at 1..N.
	for i, in := range fundingIns {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		err := txb.PutUnlockReference(byte(1+i), ledger.ConstraintIndexLock, 0)
		glb.AssertNoError(err)
	}

	// --- Move the foundry's on-chain PRXI to the target.
	retiredOut, err := glb.BuildLockOutput(lib, foundryPRXI, target)
	glb.AssertNoError(err)
	txb.ProduceOutput(retiredOut.Bytes())

	// --- Tag-along output.
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(tagAlongOut.Bytes())

	// --- PRXI remainder back to the wallet.
	totalConsumed := foundryPRXI + fundingSum
	totalProducedFixed := foundryPRXI + feeAmount
	if totalConsumed > totalProducedFixed {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, totalConsumed-totalProducedFixed, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}

	glb.Infof("retire plan:")
	glb.Infof("   foundry chainID:  %s", chainID.String())
	glb.Infof("   foundry supply:   %s (will be destroyed)", util.Th(fIn.Supply))
	glb.Infof("   PRXI moving:      %s to %s", util.Th(foundryPRXI), target.String())
	glb.Infof("   tag-along fee:    %s to %s", util.Th(feeAmount), tagAlongSeqID.StringShort())

	if !glb.YesNoPrompt("proceed?", true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Stamp + sign AFTER the prompt so the timestamp reflects the moment of
	// submission rather than the moment we offered the prompt; otherwise a
	// slow confirmation makes the tx "born stale".
	ts := glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	foundryTs := foundryIn.ID.Timestamp().AddTicks(int(consts.TransactionPace))
	ts = base.MaximumTime(ts, foundryTs)
	for _, in := range fundingIns {
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
