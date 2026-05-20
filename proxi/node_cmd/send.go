package node_cmd

import (
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

// `proxi node send` — wallet-side single-output transfer.
//
// Target syntax (-t / --target):
//
//   a/<32-byte hex>   — sigLock target (the legacy default; -t HEX with no
//                       prefix is also still accepted by ControllerFromSource
//                       and treated as sigLock).
//   c/<32-byte hex>   — chainLock target. The produced output is locked
//                       under the standard chainLock, spendable by the
//                       controller of the given chainID.
//
// Modes:
//
//   plain (default)        — produce a sigLock or chainLock output, depending
//                            on -t. The output is immediately spendable by
//                            the target.
//   --deadline             — produce a sendWithDeadline output. The target
//                            (sigLock OR chainLock) has --acceptance-slots
//                            slots to claim; after that, the wallet
//                            (master) has the configured reclaim window;
//                            after --cleanup-slots, anyone can purge.
//
// `proxi node transfer` is kept as a deprecated alias that delegates to
// `send` so existing scripts keep working.

const (
	defaultAcceptanceSlots uint32 = 60
	defaultCleanupSlots    uint32 = 8000
)

func initSendCmd() *cobra.Command {
	sendCmd := &cobra.Command{
		Use:   "send <amount>",
		Short: "send tokens from the wallet to a sigLock holder or a chainLock chain",
		Long: `Send <amount> tokens to a target identified by -t / --target.

Target syntax:
  a/<32-byte hex>   sigLock target — the produced output is locked to the
                    holder whose ED25519 holderID == that 32-byte value.
  c/<32-byte hex>   chainLock target — the output is locked under the
                    standard chainLock, spendable by the controller of
                    the given chainID.

Pass --deadline to produce a sendWithDeadline output instead of a plain
sigLock/chainLock output. The target then has --acceptance-slots to claim
the funds; after that, this wallet can reclaim until --cleanup-slots,
after which anyone can purge the output (see
claude/send_with_deadline_lock.md).

Pass --tag <chainID-hex> to transfer native tokens of that tag instead
of (or in addition to) PRXI. The recipient output gains a
tokenAmount(<tag>, <amount>) constraint; the tx pushes a sentinel
token(<tag>, 0x) for Phase D auditability and Σ-conservation. The wallet
must hold sufficient tokenAmount(<tag>, _) UTXOs to cover <amount>; any
remainder is returned as a new tokenAmount UTXO. --tag is incompatible
with --deadline.`,
		Args: cobra.ExactArgs(1),
		Run:  runSendCmd,
	}
	glb.AddFlagTarget(sendCmd)
	sendCmd.Flags().Bool("deadline", false, "produce a sendWithDeadline output instead of plain sigLock/chainLock")
	sendCmd.Flags().Uint32("acceptance-slots", defaultAcceptanceSlots,
		fmt.Sprintf("target's acceptance window in slots (only with --deadline; min %d)",
			ledger.SendWithDeadlineMinAcceptanceSlots))
	sendCmd.Flags().Uint32("cleanup-slots", defaultCleanupSlots,
		fmt.Sprintf("cleanup boundary in slots (only with --deadline; must exceed acceptance by ≥ %d)",
			ledger.SendWithDeadlineMinReclaimSlots))
	sendCmd.Flags().String("tag", "",
		"native-token tag (foundry chain ID, hex); transfer <amount> tokens of this tag instead of PRXI")
	sendCmd.InitDefaultHelpCmd()
	return sendCmd
}

func runSendCmd(cmd *cobra.Command, args []string) {
	// InitLedgerFromNode is still needed for display (Output._lines
	// uses the singleton via ledger.L). The wallet path itself does
	// not depend on it for construction.
	glb.InitLedgerFromNode()

	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	deadlineMode, err := cmd.Flags().GetBool("deadline")
	glb.AssertNoError(err)
	acceptanceSlots, err := cmd.Flags().GetUint32("acceptance-slots")
	glb.AssertNoError(err)
	cleanupSlots, err := cmd.Flags().GetUint32("cleanup-slots")
	glb.AssertNoError(err)
	tagHex, err := cmd.Flags().GetString("tag")
	glb.AssertNoError(err)

	// Tagged native-token transfer is a separate flow (see send_tagged.go).
	if tagHex != "" {
		glb.Assertf(!deadlineMode, "--tag is incompatible with --deadline")
		glb.Assertf(!cmd.Flags().Changed("acceptance-slots"),
			"--acceptance-slots only applies with --deadline")
		glb.Assertf(!cmd.Flags().Changed("cleanup-slots"),
			"--cleanup-slots only applies with --deadline")
		runSendTaggedCmd(amount, tagHex)
		return
	}

	wallet := glb.GetWalletData()
	glb.Infof("source: wallet account %s", wallet.Account.String())

	targetCtrl := glb.MustGetTarget()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified (set tag_along.sequencer_id)")
	feeAmount := glb.GetTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee not configured (set tag_along.fee)")

	md, err := glb.GetClient().GetSequencerData(*tagAlongSeqID)
	glb.AssertNoError(err)
	if md.MinimumFee() > feeAmount {
		feeAmount = md.MinimumFee()
	}

	// Deadline mode keeps the legacy recipe path. The
	// sendWithDeadlineLock helper is not in txbuildercore yet — see
	// claude/proxi_txbuildercore.md Phase 1 helpers gap.
	if deadlineMode {
		runSendCmdLegacyDeadline(wallet, targetCtrl, tagAlongSeqID, feeAmount, amount, acceptanceSlots, cleanupSlots)
		return
	}
	glb.Assertf(!cmd.Flags().Changed("acceptance-slots"),
		"--acceptance-slots only applies with --deadline")
	glb.Assertf(!cmd.Flags().Changed("cleanup-slots"),
		"--cleanup-slots only applies with --deadline")
	glb.Infof("mode:   plain transfer (target lock is %s)", targetCtrl.Name())
	glb.Infof("target: %s", targetCtrl.String())

	prompt := fmt.Sprintf("send will cost %s of fees paid to tag-along sequencer %s. Proceed?",
		util.Th(feeAmount), tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Wasm-style wallet build via txbuildercore + helpers.
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderID(ledger.SigLockFromED25519PrivateKey(wallet.PrivateKey))
	needed := amount + feeAmount

	// 1. Fetch sigLock inputs from the wallet via the API.
	res, err := glb.GetClient().GetOutputs(wallet.Account.ControllerID(), client.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   client.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: needed,
	})
	glb.AssertNoError(err)
	glb.Assertf(res.AvailableAmount >= needed,
		"not enough tokens: have %s, need %s", util.Th(res.AvailableAmount), util.Th(needed))

	// 2. Build the produced outputs (target + tag-along + optional remainder).
	var targetOut *txbuildercore.Output
	switch c := targetCtrl.(type) {
	case ledger.SigLock:
		targetOut, err = txbuildercore.NewSigLockOutput(lib, amount, base.HolderID(c))
	case ledger.ChainLock:
		var chainID base.ChainID
		glb.Assertf(len(c) == 32, "chainLock target must carry a 32-byte chain ID, got %d", len(c))
		copy(chainID[:], c)
		targetOut, err = txbuildercore.NewChainLockOutput(lib, amount, chainID)
	default:
		glb.Assertf(false, "plain send only supports sigLock or chainLock targets, got %s", targetCtrl.Name())
	}
	glb.AssertNoError(err)

	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)

	var remainderOut *txbuildercore.Output
	if res.AvailableAmount > needed {
		remainderOut, err = txbuildercore.NewSigLockOutput(lib, res.AvailableAmount-needed, walletHolderID)
		glb.AssertNoError(err)
	}

	// 3. Compose the transaction.
	txb := txbuildercore.New(0)
	consumedBytes := make([][]byte, 0, len(res.Outputs))
	for i, in := range res.Outputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err := txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}
	txb.ProduceOutput(targetOut.Bytes())
	txb.ProduceOutput(tagAlongOut.Bytes())
	if remainderOut != nil {
		txb.ProduceOutput(remainderOut.Bytes())
	}

	// 4. Finalize. SetTimestamp also sets TxData.UpgradeIndex via the
	//    glb-aware helper layer; here we use the txbuildercore raw
	//    setter and lookup upgrade index separately if needed (the
	//    ledger library passes upgrade index = 0 for now). Pace
	//    constraint is enforced server-side at parse + partial
	//    validation; using TimeNow() suffices for most paths.
	txb.SetTimestamp(ledger.TimeNow())
	txb.ComputeInputCommitment()
	txb.SignED25519(wallet.PrivateKey)

	txBytes := txb.Bytes()
	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}
	glb.Infof("transaction submitted successfully")

	if glb.NoWait() {
		return
	}
	// Derive the tx ID for inclusion tracking.
	txID, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)
	glb.TrackTxInclusion(txID, time.Second)
}

// runSendCmdLegacyDeadline keeps the --deadline path on the legacy
// glb.TransferFromED25519Wallet recipe. The wasm-style refactor for
// sendWithDeadlineLock is deferred (no txbuildercore helper yet).
func runSendCmdLegacyDeadline(wallet glb.WalletData, targetCtrl ledger.Controller, tagAlongSeqID *base.ChainID, feeAmount, amount uint64, acceptanceSlots, cleanupSlots uint32) {
	targetLock := buildSendWithDeadlineLock(wallet, targetCtrl, acceptanceSlots, cleanupSlots)
	glb.Infof("mode:   sendWithDeadline (acceptance=%d slots, cleanup=%d slots)",
		acceptanceSlots, cleanupSlots)
	glb.Infof("target: %s", describeSWDTarget(targetCtrl))

	prompt := fmt.Sprintf("send will cost %s of fees paid to tag-along sequencer %s. Proceed?",
		util.Th(feeAmount), tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := glb.TransferFromED25519Wallet(glb.TransferFromED25519WalletParams{
		WalletPrivateKey: wallet.PrivateKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      feeAmount,
		Amount:           amount,
		Target:           targetLock,
	})
	if txCtx != nil {
		glb.Verbosef("-------- send transaction ---------\n%s\n----------------", txCtx.String())
	}
	glb.AssertNoError(err)
	glb.Assertf(txCtx != nil, "inconsistency: txCtx == nil")
	glb.Infof("transaction submitted successfully")

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txCtx.ID(), time.Second)
}

// =============================================================================
// helpers
// =============================================================================

// buildSendWithDeadlineLock turns the parsed -t Controller into a
// SendWithDeadlineLock. The master is the wallet's holderID; the target
// is derived from the Controller kind:
//
//   sigLock target   → targetType=0x00, targetID = holder ID
//   chainLock target → targetType=0x01, targetID = chain ID
func buildSendWithDeadlineLock(wallet glb.WalletData, target ledger.Controller, acceptanceSlots, cleanupSlots uint32) *ledger.SendWithDeadlineLock {
	masterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(wallet.PrivateKey))

	var targetID base.HolderID
	var targetType byte
	switch c := target.(type) {
	case ledger.SigLock:
		copy(targetID[:], c[:])
		targetType = ledger.SendWithDeadlineTargetSigLock
	case ledger.ChainLock:
		// ChainLock is a 32-byte []byte holding a chain ID.
		glb.Assertf(len(c) == 32, "ChainLock controller must carry a 32-byte chain ID, got %d bytes", len(c))
		copy(targetID[:], c)
		targetType = ledger.SendWithDeadlineTargetChainLock
	default:
		glb.Assertf(false, "--deadline only supports sigLock or chainLock targets, got %s", target.Name())
	}
	return &ledger.SendWithDeadlineLock{
		MasterID:        masterID,
		TargetID:        targetID,
		TargetType:      targetType,
		AcceptanceSlots: acceptanceSlots,
		CleanupSlots:    cleanupSlots,
	}
}

// describeSWDTarget returns a human-readable form like
// "a/<hex>" or "c/<hex>" describing the target controller.
func describeSWDTarget(target ledger.Controller) string {
	return target.Source()
}
