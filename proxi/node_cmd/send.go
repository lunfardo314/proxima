package node_cmd

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

// `proxi node send` — wallet-side single-output transfer. DEPRECATED in
// favour of `send_to_wallet` and `send_to_chain` (send_to.go), which fix the
// target kind by command and check the target against the ledger; the
// modes below are shared by all three through runSend.
//
// Target syntax (-t / --target):
//
//   a/<32-byte hex>   — sigLock target.
//   c/<24-byte hex>   — chain target. The produced output is a tag-along to
//                       that chain, never a chainLock (see glb.BuildTransferOutput).
//
// Modes:
//
//   plain (default)        — produce a sigLock or tag-along output, depending
//                            on -t.
//   --deadline             — produce a sendWithDeadline output. The target
//                            (sigLock OR chainLock) has --acceptance-slots
//                            slots to claim; after that, this wallet (master)
//                            has the configured reclaim window;
//                            after --cleanup-slots, anyone can purge.
//   --tag <chainID-hex>    — native-token transfer of that tag instead of
//                            (or in addition to) the base token. Delegates to
//                            send_tagged.go. Incompatible with --deadline.

const (
	defaultAcceptanceSlots uint32 = 60
	defaultCleanupSlots    uint32 = 8000
)

// sendModesHelp documents the flags shared by send, send_to_wallet and send_to_chain.
const sendModesHelp = `
Pass --deadline to produce a sendWithDeadline output instead of a plain
sigLock/tag-along output. The target then has --acceptance-slots to claim
the funds; after that, this wallet can reclaim until --cleanup-slots,
after which anyone can purge the output (see
kb/archive/shipped/send_with_deadline_lock.md).

Pass --return <amount> (only with --deadline) to attach a
returnToSender(<amount>) constraint to the output: the target can only
accept the funds by returning <amount> base tokens to this wallet in the
same transaction (useful to send a small net amount above a large storage
deposit, or to sell tokens for a fixed price — see
kb/archive/shipped/return_to_sender.md). The command refuses if <amount> is below the
minimum storage deposit of the return receipt the target must build.

Pass --tag <chainID-hex> to transfer native tokens of that tag instead
of (or in addition to) the base token. The recipient output gains a
tokenAmount(<tag>, <amount>) constraint; the tx pushes a sentinel
token(<tag>, 0x) for Phase D auditability and Σ-conservation. The wallet
must hold sufficient tokenAmount(<tag>, _) UTXOs to cover <amount>; any
remainder is returned as a new tokenAmount UTXO. --tag is incompatible
with --deadline and accepts a wallet target only.

proxi never produces a chainLock output: tokens locked to a chain are lost
for good if the chain is deleted. A transfer to a chain is a tag-along
output, which the chain can take within the tag-along window and which the
sending wallet reclaims afterwards with 'proxi node compact'.`

func initSendCmd() *cobra.Command {
	sendCmd := &cobra.Command{
		Use:   "send <amount>",
		Short: "send tokens from the wallet to a sigLock holder or to a chain (deprecated)",
		Long: `DEPRECATED: use 'send_to_wallet <amount> <holder ID>' for a wallet target
or 'send_to_chain <amount> <chain ID>' for a chain target. Both take the
raw hex ID without the a/ or c/ prefix, check the target against the ledger
before sending, and accept the same flags as this command.

Send <amount> tokens to a target identified by -t / --target.

Target syntax:
  a/<32-byte hex>   sigLock target — the produced output is locked to the
                    holder whose ED25519 holderID == that 32-byte value.
  c/<24-byte hex>   chain target — the output is a tag-along to that chain.
` + sendModesHelp,
		Deprecated: "use 'send_to_wallet <amount> <holder ID>' for a wallet target or " +
			"'send_to_chain <amount> <chain ID>' for a chain target. " +
			"The new commands take the raw hex ID without prefix, check the target against " +
			"the ledger first, and accept the same flags.",
		Args: cobra.ExactArgs(1),
		Run:  runSendCmd,
	}
	glb.AddFlagTarget(sendCmd)
	addSendFlags(sendCmd)
	sendCmd.InitDefaultHelpCmd()
	return sendCmd
}

// addSendFlags registers the mode flags shared by send, send_to_wallet and send_to_chain.
func addSendFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("deadline", false, "produce a sendWithDeadline output instead of plain sigLock/tag-along")
	cmd.Flags().Uint32("acceptance-slots", defaultAcceptanceSlots,
		fmt.Sprintf("target's acceptance window in slots (only with --deadline; min %d)",
			ledger.SendWithDeadlineMinAcceptanceSlots))
	cmd.Flags().Uint32("cleanup-slots", defaultCleanupSlots,
		fmt.Sprintf("cleanup boundary in slots (only with --deadline; must exceed acceptance by ≥ %d)",
			ledger.SendWithDeadlineMinReclaimSlots))
	cmd.Flags().String("tag", "",
		"native-token tag (foundry chain ID, hex); transfer <amount> tokens of this tag instead of the base token")
	cmd.Flags().Uint64("return", 0,
		"attach returnToSender(<amount>): the target must return <amount> base tokens to this wallet to accept (only with --deadline)")
}

func parseAmountArg(arg string) uint64 {
	amount, err := strconv.ParseUint(arg, 10, 64)
	glb.Assertf(err == nil, "invalid amount '%s': %v", arg, err)
	glb.Assertf(amount > 0, "amount must be positive")
	return amount
}

func runSendCmd(cmd *cobra.Command, args []string) {
	runSend(cmd, parseAmountArg(args[0]), glb.MustGetTarget())
}

// runSend is the transfer body shared by send, send_to_wallet and send_to_chain:
// the caller has resolved the target controller, the mode comes from the flags.
func runSend(cmd *cobra.Command, amount uint64, targetCtrl ledger.Controller) {
	deadlineMode, err := cmd.Flags().GetBool("deadline")
	glb.AssertNoError(err)
	acceptanceSlots, err := cmd.Flags().GetUint32("acceptance-slots")
	glb.AssertNoError(err)
	cleanupSlots, err := cmd.Flags().GetUint32("cleanup-slots")
	glb.AssertNoError(err)
	tagHex, err := cmd.Flags().GetString("tag")
	glb.AssertNoError(err)
	returnAmount, err := cmd.Flags().GetUint64("return")
	glb.AssertNoError(err)

	// Tagged native-token transfer is a separate flow (see send_tagged.go).
	if tagHex != "" {
		glb.Assertf(!deadlineMode, "--tag is incompatible with --deadline")
		glb.Assertf(!cmd.Flags().Changed("acceptance-slots"),
			"--acceptance-slots only applies with --deadline")
		glb.Assertf(!cmd.Flags().Changed("cleanup-slots"),
			"--cleanup-slots only applies with --deadline")
		glb.Assertf(!cmd.Flags().Changed("return"),
			"--return only applies with --deadline")
		runSendTaggedCmd(amount, tagHex, targetCtrl)
		return
	}
	if !deadlineMode {
		glb.Assertf(!cmd.Flags().Changed("acceptance-slots"),
			"--acceptance-slots only applies with --deadline")
		glb.Assertf(!cmd.Flags().Changed("cleanup-slots"),
			"--cleanup-slots only applies with --deadline")
		glb.Assertf(!cmd.Flags().Changed("return"),
			"--return only applies with --deadline")
	}

	wallet := glb.GetWalletData()
	glb.Infof("source: wallet account %s", wallet.Account.String())

	// manage tag along data

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified (set tag_along.sequencer_id)")

	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)
	glb.Assertf(feeAmount > 0, "tag-along fee resolved to 0. Fee-less option not supported yet")

	// Wallet-derived "now" — wall-clock mapped through the genesis +
	// tick-duration constants. Singleton-free equivalent of
	// ledger.TimeNow().Slot.
	targetSlot := glb.GetLedgerTimeNow().Slot

	// Build the recipient output for whichever mode is selected. Both
	// branches go through txbuildercore + the wallet helpers; no
	// ledger.NewOutput / ledger/txbuilder sugar reachable from here.
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(wallet.PrivateKey)

	var targetOut *txbuildercore.Output
	if deadlineMode {
		targetOut, err = buildSendWithDeadlineOutput(lib, targetCtrl, walletHolderID, amount, acceptanceSlots, cleanupSlots)
		glb.AssertNoError(err)
		glb.Infof("mode:   sendWithDeadline (acceptance=%d slots, cleanup=%d slots)",
			acceptanceSlots, cleanupSlots)
		glb.Infof("target: %s", targetCtrl.Source())

		if returnAmount > 0 {
			// Storage-deposit guard: the target accepting the funds must
			// build a return receipt (sigLock-to-master + 1-byte anti-fold
			// literal). If returnAmount is below that receipt's minimum
			// storage deposit, no consumer could ever satisfy the
			// constraint — refuse to produce a dead output.
			receiptProbe, perr := lib.NewReturnReceiptOutput(returnAmount, walletHolderID, 0)
			glb.AssertNoError(perr)
			minDeposit := minStorageDeposit(glb.GetClient(), receiptProbe)
			glb.Assertf(returnAmount >= minDeposit,
				"--return %s is below the return receipt's minimum storage deposit %s; the target could never accept this output",
				util.Th(returnAmount), util.Th(minDeposit))

			// Append returnToSender(returnAmount) at the next free slot (3).
			bld, berr := txbuildercore.OutputBuilderFromBytes(targetOut.Bytes())
			glb.AssertNoError(berr)
			rtsBin, cerr := lib.NewReturnToSenderBytecode(returnAmount)
			glb.AssertNoError(cerr)
			bld.MustPushConstraint(rtsBin)
			targetOut = bld.Output()
			glb.Infof("return: target must return %s to %s to accept", util.Th(returnAmount), wallet.Account.String())
		}
	} else {
		targetOut, err = glb.BuildTransferOutput(lib, amount, targetCtrl, walletHolderID)
		glb.AssertNoError(err)
		if _, toChain := targetCtrl.(ledger.ChainLock); toChain {
			glb.Infof("mode:   plain transfer to a chain: tag-along output, reclaimable by this wallet after %d slots",
				glb.GetLedgerConstants().TagAlongSlots)
		} else {
			glb.Infof("mode:   plain transfer (target lock is %s)", targetCtrl.Name())
		}
		glb.Infof("target: %s", targetCtrl.String())
	}

	// Fetch sigLock-owned wallet inputs covering amount + fee.
	needed := amount + feeAmount
	res, err := glb.GetClient().GetOutputsForControllerID(wallet.Account.ControllerID(), client.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   client.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: needed,
	})
	glb.AssertNoError(err)
	glb.Assertf(res.AvailableAmount >= needed,
		"not enough tokens: have %s, need %s", util.Th(res.AvailableAmount), util.Th(needed))

	prompt := fmt.Sprintf("send will cost %s of fees paid to tag-along sequencer %s. Proceed?",
		util.Th(feeAmount), tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt(prompt, true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txBytes, txid, consumed, err := makeSendTransaction(
		wallet.PrivateKey, res.Outputs, targetOut, amount,
		*tagAlongSeqID, feeAmount, targetSlot)
	glb.AssertNoError(err)
	glb.Assertf(txBytes != nil, "something wrong: empty send tx")

	if err := glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		os.Exit(1)
	}
	glb.Infof("transaction %s submitted successfully", txid.StringShort())

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}

// buildSendWithDeadlineOutput composes the recipient SWD output from
// the target Controller. The master is the wallet's holderID; the
// target is derived from the Controller kind (sigLock holder bytes
// for sigLock targets, raw chainID bytes for chain targets).
func buildSendWithDeadlineOutput(
	lib *txbuildercore.Library[any],
	targetCtrl ledger.Controller,
	masterID base.HolderID,
	amount uint64,
	acceptanceSlots, cleanupSlots uint32,
) (*txbuildercore.Output, error) {
	var (
		targetID   base.HolderID
		targetType byte
	)
	switch c := targetCtrl.(type) {
	case ledger.SigLock:
		copy(targetID[:], c[:])
		targetType = txbuildercore.SendWithDeadlineTargetSigLock
	case ledger.ChainLock:
		// the 24-byte chain ID occupies the first bytes of the 32-byte target field
		if len(c) != base.ChainIDLength {
			return nil, fmt.Errorf("--deadline chain target must carry a %d-byte chain ID, got %d", base.ChainIDLength, len(c))
		}
		copy(targetID[:], c)
		targetType = txbuildercore.SendWithDeadlineTargetChainLock
	default:
		return nil, fmt.Errorf("--deadline only supports sigLock or chainLock targets, got %s", targetCtrl.Name())
	}
	return lib.NewSendWithDeadlineOutput(txbuildercore.SendWithDeadlineOutputParams{
		Amount:          amount,
		MasterID:        masterID,
		TargetID:        targetID,
		TargetType:      targetType,
		AcceptanceSlots: acceptanceSlots,
		CleanupSlots:    cleanupSlots,
	})
}

// minStorageDeposit returns the minimum storage deposit for `out`, computed
// wallet-side: effective size (utxoBytes + indexValuesTupleBytes + N*33,
// mirroring ledger.effectiveStorageSize) fed through the `storageDeposit($0)`
// schedule via /eval. Same approach as foundry's computeStorageDeposit.
func minStorageDeposit(c *client.APIClient, out *txbuildercore.Output) uint64 {
	size := uint64(len(out.Bytes()))
	if ivBin, err := out.ConstraintAt(ledger.ConstraintIndexIndexValues); err == nil && len(ivBin) > 0 {
		values, verr := ledger.IndexValuesFromBytes(ivBin)
		glb.AssertNoError(verr)
		size += uint64(len(ivBin)) + uint64(len(values))*33
	}
	deposit, err := c.EvalU64(0, fmt.Sprintf("storageDeposit(u64/%d)", size))
	glb.AssertNoError(err)
	return deposit
}

// makeSendTransaction is the pure wasm-wallet compose helper for
// `proxi node send`: consumes the supplied sigLock-owned wallet
// inputs and produces the recipient output, the tag-along fee output
// and an optional remainder back to the wallet. No I/O; no
// ledger.L() singleton; no ledger/txbuilder sugar.
//
// Input unlock pattern: PutSignatureUnlock(0) on input 0 (carries
// the tx signature) + PutUnlockReference(i, ConstraintIndexLock, 0)
// on the rest. The reference path makes the on-chain `_sigLock`
// constraint short-circuit through `unlockedByReference` for the
// homogeneous inputs 1..N — same holderID + same lock bytecode —
// skipping one txHolderID(...) hash compare per referenced input.
func makeSendTransaction(
	walletPrivateKey ed25519.PrivateKey,
	walletOutputs []*ledger.OutputWithID,
	targetOut *txbuildercore.Output,
	targetAmount uint64,
	tagAlongSeqID base.ChainID,
	tagAlongFee uint64,
	targetSlot uint32,
) (txBytes []byte, txid base.TransactionID, consumed [][]byte, err error) {
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletPrivateKey)
	txb := txbuildercore.New(0)

	inTotal := uint64(0)
	consumed = make([][]byte, 0, len(walletOutputs))
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			if err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
				return nil, base.TransactionID{}, nil, err
			}
		}
		inTotal += in.Output.TokenBalance()
	}
	if inTotal < targetAmount+tagAlongFee {
		return nil, base.TransactionID{}, nil, fmt.Errorf("not enough balance: have %d, need %d",
			inTotal, targetAmount+tagAlongFee)
	}

	txb.ProduceOutput(targetOut.Bytes())

	taOut, err := txbuildercore.NewTagAlongOutput(lib, tagAlongFee, tagAlongSeqID, walletHolderID)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	txb.ProduceOutput(taOut.Bytes())

	if inTotal > targetAmount+tagAlongFee {
		remainderOut, rerr := txbuildercore.NewSigLockOutput(lib, inTotal-targetAmount-tagAlongFee, walletHolderID)
		if rerr != nil {
			return nil, base.TransactionID{}, nil, rerr
		}
		txb.ProduceOutput(remainderOut.Bytes())
	}

	txb.SetTimestamp(base.T(targetSlot, 10))
	txb.ComputeInputCommitment()
	txb.SignED25519(walletPrivateKey)

	txBytes = txb.Bytes()
	txid, err = txbuildercore.TxIDFromBytes(txBytes)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	return txBytes, txid, consumed, nil
}
