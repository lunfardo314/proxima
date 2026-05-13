package foundry

import (
	"encoding/hex"
	"os"
	"strconv"
	"strings"
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
  - lock at slot 2 (the target chosen with -t / --target)
  - chain origin at slot 3
  - foundry(NilChainID, --initial-supply) at slot 4
  - optional raw policy bytecode at slot 5 (use --policy 0x<hex>)

The foundry's tag — and therefore the native-token tag — equals the
chain ID, computed as blake2b(originOutputID). At origin the foundry
records tag = NilChainID; the first foundry transit replaces it with
the real chain ID and enforces the tag-equals-chain-ID invariant from
then on.`,
		Args: cobra.ExactArgs(1),
		Run:  runFoundryCreateCmd,
	}
	glb.AddFlagTarget(cmd)
	cmd.Flags().Uint64("initial-supply", 0, "initial circulating supply stored on the foundry at origin")
	cmd.Flags().String("policy", "", "optional policy script bytecode (0x-prefixed hex); immutable across transits once set")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runFoundryCreateCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()
	glb.Infof("wallet account: %s", walletData.Account.String())

	onChainAmount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)
	initialSupply, err := cmd.Flags().GetUint64("initial-supply")
	glb.AssertNoError(err)
	policyHex, err := cmd.Flags().GetString("policy")
	glb.AssertNoError(err)

	var policyBytes []byte
	if policyHex != "" {
		policyBytes, err = hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(policyHex, "0x"), "0X"))
		glb.Assertf(err == nil, "failed parsing --policy: %v", err)
		glb.Assertf(len(policyBytes) > 0, "--policy must decode to non-empty bytes (omit the flag to leave slot 5 absent)")
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

	foundryOut := txbuilder.MakeFoundryOriginOutput(onChainAmount, target, ts.Slot, initialSupply, policyBytes)
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
	glb.Infof("   initial supply:    %s", util.Th(initialSupply))
	if len(policyBytes) > 0 {
		glb.Infof("   policy script:     %d bytes", len(policyBytes))
	} else {
		glb.Infof("   policy script:     (none)")
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
