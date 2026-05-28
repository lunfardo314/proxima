package foundry

import (
	"fmt"
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

func initFoundryCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "create a new foundry origin (chain origin + foundry constraint)",
		Long: `Create a new foundry chain origin. The produced output carries:
  - amounts (PRXI on-chain balance)
  - lock at index 2 (target chosen with -t, defaults to wallet account)
  - chain origin at index 3
  - foundry(0) at index 4
  - optional predefined policy script bytecode at index 5

On-chain balance defaults to twice the minimum storage deposit for the
produced foundry-origin output; override with --balance.

The foundry's tag (and therefore the native-token tag) IS the sibling
chain constraint's ChainID, computed as blake2b(originOutputID) at first
transit. At origin the chain ID is still NilChainID; supply starts at 0.
The first foundry transit ("mint") emits the initial circulating supply
once the chain ID has become real.

By default a foundry is NOT delegatable: an immutable inline guard script
is appended after foundry() that requires the controller lock to stay a
sigLock across every transit (the controller can still be changed to a
different sigLock; only swapping it to a delegateLock is rejected). Pass
--allow_delegation to omit the guard and permit delegation / a non-sigLock
controller.

Post-foundry() constraints form an AND-ed policy list with no fixed
positions: the guard is appended after any predefined policy script.

Policy options (mutually exclusive — at most one of these flags):
  --non-destructible      attach foundryNonDestructible. The foundry chain
                          can only be discontinued when its supply is 0
                          (all tokens must be burned back first). The
                          policy script self-locks across every transit.
  --max-supply N          attach foundryMaxSupply(N). On every transit the
                          produced foundry supply must be <= N. Self-locks.`,
		Args: cobra.NoArgs,
		Run:  runFoundryCreateCmd,
	}
	glb.AddFlagTarget(cmd)
	cmd.Flags().Bool("non-destructible", false, "attach the foundryNonDestructible predefined policy script")
	cmd.Flags().Uint64("max-supply", 0, "attach the foundryMaxSupply(N) predefined policy script with cap N")
	cmd.Flags().Uint64("balance", 0, "explicit on-chain balance in PRXI; 0 (default) uses 2x minimum storage deposit")
	cmd.Flags().Bool("allow_delegation", false, "omit the default sigLock-controller guard, permitting delegation / a non-sigLock controller")
	cmd.InitDefaultHelpCmd()
	return cmd
}

// foundrySigLockGuardSource is an inline policy script (composed only from
// existing library symbols — nothing is added to the ledger library) appended
// after foundry() by default. It self-locks at its own position across every
// transit and requires the controller lock at lockConstraintIndex to remain a
// sigLock, which bans swapping it to a delegateLock — i.e. bans delegating the
// foundry. Changing the controller to a DIFFERENT sigLock still passes (only
// the lock kind is checked, not the holder).
const foundrySigLockGuardSource = "and(selfImmutableOnSuccessorIndex(selfBlockIndex),or(not(selfIsProducedOutput),require(equal(parseBytecode(selfSiblingConstraint(lockConstraintIndex),0x),#sigLock),!!!foundry_expects_siglock)))"

func runFoundryCreateCmd(cmd *cobra.Command, _ []string) {
	walletData := glb.GetWalletData()
	glb.Infof("wallet account: %s", walletData.Account.String())

	balanceFlag, err := cmd.Flags().GetUint64("balance")
	glb.AssertNoError(err)

	nonDestructible, err := cmd.Flags().GetBool("non-destructible")
	glb.AssertNoError(err)
	maxSupply, err := cmd.Flags().GetUint64("max-supply")
	glb.AssertNoError(err)
	glb.Assertf(!(nonDestructible && maxSupply > 0),
		"--non-destructible and --max-supply are mutually exclusive: only one predefined policy script can be attached")

	allowDelegation, err := cmd.Flags().GetBool("allow_delegation")
	glb.AssertNoError(err)

	target := glb.MustGetTarget()

	// By default the foundry gets a sigLock-controller guard. It requires the
	// controller to be a sigLock at origin, so a non-sigLock target is only
	// valid together with --allow_delegation.
	if !allowDelegation {
		_, isSig := target.(ledger.SigLock)
		glb.Assertf(isSig, "foundry controller must be a sigLock unless --allow_delegation is set (got %s)", target.Name())
	}

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	client := glb.GetClient()

	// Wasm-style build via txbuildercore + helpers.
	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)

	// Compile optional foundry policy bytecode via the wallet library.
	var policyBytes []byte
	switch {
	case nonDestructible:
		policyBytes, err = lib.CompileExpression(ledger.FoundryNonDestructibleName)
		glb.AssertNoError(err)
	case maxSupply > 0:
		policyBytes, err = lib.CompileExpression(fmt.Sprintf("%s(u64/%d)", ledger.FoundryMaxSupplyName, maxSupply))
		glb.AssertNoError(err)
	}

	// Compile the default sigLock-controller guard unless delegation is allowed.
	var guardBytes []byte
	if !allowDelegation {
		guardBytes, err = lib.CompileExpression(foundrySigLockGuardSource)
		glb.AssertNoError(err)
	}

	// buildFoundryOriginOutput composes the foundry chain-origin output for the
	// given on-chain balance. originSlot must equal the tx timestamp slot,
	// so the slot is parameterised — we size with a tentative slot for
	// storage-deposit calc, then rebuild with the finalised slot post-prompt.
	buildFoundryOriginOutput := func(amount uint64, slot uint32) *txbuildercore.Output {
		baseOut, err := glb.BuildLockOutput(lib, amount, target)
		glb.AssertNoError(err)
		fb, err := txbuildercore.OutputBuilderFromBytes(baseOut.Bytes())
		glb.AssertNoError(err)
		chainOriginBin, err := lib.NewChainOrigin(slot)
		glb.AssertNoError(err)
		fb.PutConstraint(chainOriginBin, ledger.ConstraintIndexChain)
		foundryBin, err := lib.NewFoundryBytecode(0)
		glb.AssertNoError(err)
		fb.PutConstraint(foundryBin, ledger.ConstraintIndexFoundry)
		if len(policyBytes) > 0 {
			fb.PutConstraint(policyBytes, ledger.ConstraintIndexFoundryPolicy)
		}
		// Append post-foundry policies (no fixed position): the sigLock guard
		// lands right after foundry() or after the predefined policy.
		if len(guardBytes) > 0 {
			fb.MustPushConstraint(guardBytes)
		}
		return fb.Output()
	}

	onChainAmount := balanceFlag
	defaultedFromDeposit := false
	if onChainAmount == 0 {
		// Probe the storage deposit using a placeholder amount; the 2x doubling
		// covers the few-byte size delta when the real amount is encoded.
		probeSlot := consts.LedgerTimeFromClockTime(time.Now()).Slot
		deposit := computeStorageDeposit(client, buildFoundryOriginOutput(1, probeSlot))
		onChainAmount = 2 * deposit
		defaultedFromDeposit = true
	}

	needed := onChainAmount + feeAmount
	res, err := client.GetOutputsForControllerID(walletData.Account.ControllerID(), apiclient.GetOutputsParams{
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

	// Precompute the timestamp floor from the inputs (pure data, no clock).
	var maxInputTs base.LedgerTime
	for _, in := range walletOutputs {
		maxInputTs = base.MaximumTime(maxInputTs, in.Timestamp())
	}

	glb.Infof("creating new foundry chain origin:")
	if defaultedFromDeposit {
		glb.Infof("   on-chain balance:  %s  (default: 2x min storage deposit)", util.Th(onChainAmount))
	} else {
		glb.Infof("   on-chain balance:  %s", util.Th(onChainAmount))
	}
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
	if allowDelegation {
		glb.Infof("   delegation:        ALLOWED (no sigLock-controller guard)")
	} else {
		glb.Infof("   delegation:        disabled (sigLock-controller guard attached, %d bytes)", len(guardBytes))
	}
	glb.Infof("   tag-along fee:     %s to %s", util.Th(feeAmount), tagAlongSeqID.StringShort())
	glb.Infof("   future chain ID:   derived from final tx ID (printed after submission)")

	if !glb.YesNoPrompt("proceed?", true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Stamp + build + sign AFTER the prompt so the timestamp reflects the moment
	// of submission. The chain origin's originSlot must equal the tx slot
	// (chain.easyfl `equalUint($2, txSlot)`), so the foundry origin output is
	// rebuilt with the finalised slot here.
	ts := consts.LedgerTimeFromClockTime(time.Now())
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	ts = base.MaximumTime(ts, maxInputTs)

	txb := txbuildercore.New(0)
	consumedBytes := make([][]byte, 0, len(walletOutputs))
	consumedTotal := uint64(0)
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		consumedTotal += in.Output.TokenBalance()
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err := txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}

	foundryIdx := txb.ProduceOutput(buildFoundryOriginOutput(onChainAmount, ts.Slot).Bytes())

	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(tagAlongOut.Bytes())

	if consumedTotal > onChainAmount+feeAmount {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, consumedTotal-onChainAmount-feeAmount, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletData.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)

	foundryOid, err := base.NewOutputID(txid, foundryIdx)
	glb.AssertNoError(err)
	chainID := base.MakeOriginChainID(foundryOid)

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}
	glb.Infof("transaction submitted: %s", txid.String())
	glb.Infof("future chain ID:       %s", chainID.String())

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}

// computeStorageDeposit returns the minimum storage deposit for the given
// output. Effective size matches ledger.effectiveStorageSize:
//
//	utxoBytes + indexValuesTupleBytes + N*33
//
// then the schedule (`storageDeposit($0)`) is evaluated server-side via /eval.
func computeStorageDeposit(c *apiclient.APIClient, out *txbuildercore.Output) uint64 {
	size := uint64(len(out.Bytes()))
	if ivBin, err := out.ConstraintAt(ledger.ConstraintIndexIndexValues); err == nil && len(ivBin) > 0 {
		values, err := ledger.IndexValuesFromBytes(ivBin)
		glb.AssertNoError(err)
		size += uint64(len(ivBin)) + uint64(len(values))*33
	}
	deposit, err := c.EvalU64(0, fmt.Sprintf("storageDeposit(u64/%d)", size))
	glb.AssertNoError(err)
	return deposit
}
