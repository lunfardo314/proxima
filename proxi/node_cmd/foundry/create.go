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

func initFoundryCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <on-chain balance>",
		Short: "create a new foundry origin (chain origin + foundry constraint)",
		Long: `Create a new foundry chain origin. The produced output carries:
  - amounts (PRXI on-chain balance)
  - lock at index 2 (target chosen with -t, defaults to wallet account)
  - chain origin at index 3
  - foundry(NilChainID, 0) at index 4
  - optional predefined policy script bytecode at index 5

The foundry's tag (and therefore the native-token tag) equals the chain
ID, computed as blake2b(originOutputID). At origin the foundry records
tag = NilChainID and supply = 0; the first foundry transit ("mint")
replaces the tag with the real chain ID and produces the initial circulating
supply.

Policy options (mutually exclusive — at most one of these flags):
  --non-destructible      attach foundryNonDestructible. The foundry chain
                          can only be discontinued when its supply is 0
                          (all tokens must be burned back first). The
                          policy script self-locks across every transit.
  --max-supply N          attach foundryMaxSupply(N). On every transit the
                          produced foundry supply must be <= N. Self-locks.

If no policy flag is set, index 5 is left empty and the foundry is
unconstrained beyond the foundry() invariants.`,
		Args: cobra.ExactArgs(1),
		Run:  runFoundryCreateCmd,
	}
	glb.AddFlagTarget(cmd)
	cmd.Flags().Bool("non-destructible", false, "attach the foundryNonDestructible predefined policy script")
	cmd.Flags().Uint64("max-supply", 0, "attach the foundryMaxSupply(N) predefined policy script with cap N")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runFoundryCreateCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()
	glb.Infof("wallet account: %s", walletData.Account.String())

	onChainAmount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	nonDestructible, err := cmd.Flags().GetBool("non-destructible")
	glb.AssertNoError(err)
	maxSupply, err := cmd.Flags().GetUint64("max-supply")
	glb.AssertNoError(err)
	glb.Assertf(!(nonDestructible && maxSupply > 0),
		"--non-destructible and --max-supply are mutually exclusive: only one predefined policy script can be attached")

	var policyBytes []byte
	switch {
	case nonDestructible:
		policyBytes = ledger.FoundryNonDestructibleBytecode()
	case maxSupply > 0:
		policyBytes = ledger.FoundryMaxSupplyBytecode(maxSupply)
	}

	target := glb.MustGetTarget()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	client := glb.GetClient()
	needed := onChainAmount + feeAmount
	res, err := client.GetOutputs(walletData.Account.ControllerID(), apiclient.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   apiclient.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: needed,
	})
	glb.AssertNoError(err)
	glb.PrintLRB(&res.LRBID)
	glb.Assertf(res.AvailableAmount >= needed, "not enough tokens. Need %s, have %s",
		util.Th(needed), util.Th(res.AvailableAmount))
	walletOutputs := res.Outputs

	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}

	txb := txbuilder.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(walletOutputs...)
	glb.AssertNoError(err)
	ts = base.MaximumTime(inTs, ts)
	for i := range walletOutputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}

	// At origin, supply is always 0 — the real chain ID is not known
	// until the tx is finalised, so no tokenAmount outputs can be tagged
	// in the same tx. Minting happens at a separate (later) foundry
	// transit.
	foundryOut := txbuilder.MakeFoundryOriginOutput(onChainAmount, target, ts.Slot, 0, policyBytes)
	glb.AssertNoError(foundryOut.EnoughAmountForStorageDeposit())
	foundryIdx, err := txb.ProduceOutput(foundryOut)
	glb.AssertNoError(err)

	outTagAlong := ledger.NewTagAlongOutput(feeAmount, *tagAlongSeqID, base.HolderID(walletData.Account))
	_, err = txb.ProduceOutput(outTagAlong)
	glb.AssertNoError(err)

	totalConsumed := txb.ConsumedAmount()
	totalProduced, _ := txb.ProducedAmount()
	if totalConsumed > totalProduced {
		remainder := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(totalConsumed - totalProduced).WithLock(walletData.Account)
		})
		_, err = txb.ProduceOutput(remainder)
		glb.AssertNoError(err)
	}

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(walletData.PrivateKey)

	txBytes, txid, failedTx, err := txb.BytesWithValidation()
	glb.Assertf(err == nil, "build failed: %v\n---------- failing tx --------\n%s", err, failedTx)

	foundryOid, err := base.NewOutputID(txid, foundryIdx)
	glb.AssertNoError(err)
	chainID := base.MakeOriginChainID(foundryOid)

	glb.Infof("creating new foundry chain origin:")
	glb.Infof("   on-chain balance:  %s", util.Th(onChainAmount))
	glb.Infof("   initial supply:    0  (mint with a separate command)")
	switch {
	case nonDestructible:
		glb.Infof("   policy:            foundryNonDestructible (%d bytes)", len(policyBytes))
	case maxSupply > 0:
		glb.Infof("   policy:            foundryMaxSupply(%s) (%d bytes)", util.Th(maxSupply), len(policyBytes))
	default:
		glb.Infof("   policy:            (none)")
	}
	glb.Infof("   chain controller:  %s", target.String())
	glb.Infof("   tag-along fee:     %s to %s", util.Th(feeAmount), tagAlongSeqID.StringShort())
	glb.Infof("   future chain ID:   %s", chainID.String())

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
